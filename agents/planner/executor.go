package planner

import (
	"ai/agents"
	"ai/agents/acceptor"
	"ai/agents/backendlead"
	"ai/agents/codereviewer"
	"ai/agents/developer"
	"ai/agents/devops"
	"ai/agents/devopslead"
	"ai/agents/frontendlead"
	"ai/agents/qaengineer"
	"ai/agents/qalead"
	"ai/checkpoint"
	"ai/forges"
	"ai/logging"
	"ai/models"
	"ai/projects"
	"ai/runner"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Executor выполняет план поэтапно, создавая нужных агентов.
// Каждый шаг запускается отдельным агентом с чистым контекстом:
// история не накапливается между шагами, что снижает расход токенов.
//
// Шаги группируются в волны параллельности (ComputeWaves): внутри волны шаги
// независимы, волны идут последовательно. Сейчас шаги волны выполняются
// по одному, но структура готова к параллельному запуску (задел на будущее).
//
// При подключённом чекпоинте (SetCheckpoint) состояние выполнения после
// каждого шага сохраняется в Redis, что позволяет возобновить работу
// с места остановки (resume).
type Executor struct {
	provider models.LLMProvider
	plan     *Plan
	// completed — шаги, успешно завершённые в текущем запуске (в т.ч.
	// восстановленные из чекпоинта при resume).
	completed map[string]bool
	statuses  map[string]string
	// acceptReports — отчёты приёмки по шагам acceptor (ID шага → отчёт).
	// Заполняются runAcceptorAgent и читаются циклом приёмки при создании
	// плана исправлений.
	acceptReports map[string]*acceptor.Report
	// store — Redis-хранилище чекпоинтов; nil — контрольные точки отключены.
	store *checkpoint.Store
	// resume — возобновлять ли выполнение с чекпоинта.
	resume bool
}

// NewExecutor создаёт исполнителя плана.
func NewExecutor(provider models.LLMProvider, plan *Plan) *Executor {
	return &Executor{
		provider:      provider,
		plan:          plan,
		completed:     make(map[string]bool),
		statuses:      make(map[string]string),
		acceptReports: make(map[string]*acceptor.Report),
	}
}

// SetCheckpoint подключает Redis-хранилище чекпоинтов и включает/выключает
// режим resume. Возвращает сам исполнитель для цепочек вызовов.
func (e *Executor) SetCheckpoint(store *checkpoint.Store, resume bool) *Executor {
	e.store = store
	e.resume = resume
	return e
}

// Run выполняет все шаги плана по волнам параллельности с учётом
// зависимостей. При resume пропускает уже завершённые шаги.
func (e *Executor) Run(ctx context.Context) error {
	waves, err := e.plan.ComputeWaves()
	if err != nil {
		return fmt.Errorf("в плане нарушен порядок шагов: %w", err)
	}

	// Инициализация чекпоинта: либо восстанавливаем состояние (resume),
	// либо начинаем с чистого листа.
	if e.store != nil {
		if err := e.initCheckpoint(ctx, waves); err != nil {
			return err
		}
	}

	for _, wave := range waves {
		for _, stepID := range wave {
			step := e.findStep(stepID)
			if step == nil {
				return fmt.Errorf("шаг %q не найден в плане", stepID)
			}

			// Resume: шаг уже завершён в прошлом запуске — пропускаем.
			if e.completed[stepID] {
				logging.Detailf("[%s] шаг %s уже выполнен ранее, пропускаю (resume)", agentLabel(step.Agent, step.Role), stepID)
				continue
			}

			logging.Infof("[%s] шаг %s: %s", agentLabel(step.Agent, step.Role), step.ID, step.Description)
			e.markRunning(ctx, stepID)
			if err := e.executeStep(ctx, step); err != nil {
				e.markFailed(ctx, stepID)
				return fmt.Errorf("шаг %q: %w", step.ID, err)
			}
			e.markDone(ctx, stepID)
			logging.Infof("[%s] шаг %s завершён", agentLabel(step.Agent, step.Role), step.ID)
		}
	}

	// Цикл приёмки: после выполнения всех шагов плана принимаем собранное
	// приложение. Если приёмка выявила ошибки — планировщик составляет шаги
	// исправления, они выполняются, и приёмка повторяется (бюджет ACCEPT_MAX_ROUNDS).
	return e.runAcceptanceLoop(ctx)
}

// initCheckpoint восстанавливает состояние при resume или создаёт новый
// снапшот и сохраняет его в Redis.
func (e *Executor) initCheckpoint(ctx context.Context, waves [][]string) error {
	if e.resume {
		if snap, err := e.store.Load(ctx); err == nil && snap != nil {
			// Восстанавливаем завершённые шаги из чекпоинта.
			for id := range snap.Completed {
				e.completed[id] = true
				e.statuses[id] = checkpoint.StatusDone
			}
			n := len(snap.Completed)
			logging.Infof("[Checkpoint] resume: восстановлено %d завершённых шагов", n)
			return nil
		} else if err != nil && err != checkpoint.ErrNotFound {
			logging.Warnf("[Checkpoint] не удалось прочитать чекпоинт (%v), начинаю заново", err)
		}
	}

	// Свежий старт: сохраняем стартовый снапшот.
	snap := e.newSnapshot(waves)
	if err := e.store.Save(ctx, snap); err != nil {
		return fmt.Errorf("сохранение стартового чекпоинта: %w", err)
	}
	return nil
}

// newSnapshot собирает стартовое состояние выполнения плана.
func (e *Executor) newSnapshot(waves [][]string) *checkpoint.Snapshot {
	planJSON, _ := json.Marshal(e.plan)
	statuses := make(map[string]string, len(e.plan.Steps))
	for _, s := range e.plan.Steps {
		statuses[s.ID] = checkpoint.StatusPending
	}
	return &checkpoint.Snapshot{
		ProjectName: e.plan.ProjectName,
		Summary:     e.plan.Summary,
		PlanJSON:    planJSON,
		Completed:   map[string]bool{},
		Statuses:    statuses,
		Waves:       waves,
	}
}

// markRunning помечает шаг как выполняющийся и сбрасывает в чекпоинт.
func (e *Executor) markRunning(ctx context.Context, stepID string) {
	e.statuses[stepID] = checkpoint.StatusRunning
	e.persistStatus(ctx, stepID, checkpoint.StatusRunning)
}

// markDone помечает шаг завершённым и сбрасывает в чекпоинт.
func (e *Executor) markDone(ctx context.Context, stepID string) {
	e.completed[stepID] = true
	e.statuses[stepID] = checkpoint.StatusDone
	e.persistStatus(ctx, stepID, checkpoint.StatusDone)
	// Шаг успешно завершён — история агентского цикла для resume больше не
	// нужна, удаляем её, чтобы не занимала место в чекпоинте.
	if e.store != nil {
		if snap, err := e.store.Load(ctx); err == nil {
			if len(snap.Conversations) > 0 {
				if _, ok := snap.Conversations[stepID]; ok {
					if err := e.store.ClearRoundState(ctx, snap, stepID); err != nil {
						logging.Detailf("[Checkpoint] ошибка очистки истории шага %s: %v", stepID, err)
					}
				}
			}
		}
	}
}

// markFailed помечает шаг упавшим и сбрасывает в чекпоинт.
func (e *Executor) markFailed(ctx context.Context, stepID string) {
	e.statuses[stepID] = checkpoint.StatusFailed
	if e.store != nil {
		// Загружаем свежий снапшот и обновляем статус.
		if snap, err := e.store.Load(ctx); err == nil {
			_ = e.store.MarkStep(ctx, snap, stepID, checkpoint.StatusFailed)
		}
	}
}

// persistStatus сохраняет статус шага в Redis (если чекпоинт подключён).
func (e *Executor) persistStatus(ctx context.Context, stepID, status string) {
	if e.store == nil {
		return
	}
	if snap, err := e.store.Load(ctx); err == nil {
		if err := e.store.MarkStep(ctx, snap, stepID, status); err != nil {
			logging.Detailf("[Checkpoint] ошибка сохранения статуса %s=%s: %v", stepID, status, err)
		}
	} else {
		logging.Detailf("[Checkpoint] ошибка чтения снапшота для %s: %v", stepID, err)
	}
}

// executeStep выполняет один шаг плана нужным агентом.
func (e *Executor) executeStep(ctx context.Context, step *Step) error {
	projectName := step.ProjectName
	if projectName == "" {
		projectName = e.plan.ProjectName
	}

	switch step.Agent {
	case AgentBackendDev, AgentFrontendDev, AgentDevops, AgentDevopsLead, AgentQAEngineer, AgentQALead, AgentFrontendLead, AgentBackendLead:
		return e.runCodingAgent(ctx, step, projectName)

	case AgentCodeReviewer:
		return e.runReviewAgent(ctx, step, projectName)

	case AgentAcceptor:
		return e.runAcceptorAgent(ctx, step, projectName)

	default:
		return fmt.Errorf("неизвестный тип агента: %s", step.Agent)
	}
}

// runCodingAgent запускает агента-разработчика (backend/frontend) или другого
// специалиста (devops, qa, лиды). Все агенты ограничены областью работы scope:
// пишут/читают только указанные файлы.
func (e *Executor) runCodingAgent(ctx context.Context, step *Step, projectName string) error {
	var agent agents.Agent
	switch step.Agent {
	case AgentBackendDev:
		agent = developer.NewBackendDeveloper(projectName, step.Prompt)
	case AgentFrontendDev:
		agent = developer.NewFrontendDeveloper(projectName, step.Prompt)
	case AgentDevops:
		agent = devops.NewDevops(projectName, step.Prompt)
	case AgentDevopsLead:
		agent = devopslead.NewDevopsLead(projectName, step.Prompt)
	case AgentQAEngineer:
		agent = qaengineer.NewQAEngineer(projectName, step.Prompt)
	case AgentQALead:
		agent = qalead.NewQALead(projectName, step.Prompt)
	case AgentFrontendLead:
		agent = frontendlead.NewFrontendLead(projectName, step.Prompt)
	case AgentBackendLead:
		agent = backendlead.NewBackendLead(projectName, step.Prompt)
	}

	// Ограничиваем инструменты агента областью работы шага: вне scope он не
	// сможет ни читать, ни писать, ни удалять файлы.
	if scoper, ok := agent.(interface{ SetScope([]string) }); ok {
		scoper.SetScope(step.Scope)
	}

	// Роль разработчика (frontend/backend): для backend/frontend-агентов она
	// определяется самим типом агента (роль в шаге хранится для журналирования);
	// если шаг несёт роль generic-агента, способному её применить — передаём
	// её агенту. Ограничивает промпт и стиль работы, но не заменяет scope.
	if rr, ok := agent.(interface{ SetRole(string) }); ok && step.Role != "" {
		rr.SetRole(string(step.Role))
	}

	// Возобновление агентского цикла: если в чекпоинте сохранена история
	// диалога (в прошлом запуске шаг упёрся в лимит раундов), передаём её
	// в цикл, чтобы продолжить с места остановки, а не начинать заново.
	genCtx := ctx
	if rs, ok := e.loadResumeState(ctx, step.ID); ok {
		logging.Detailf("[%s] шаг %s: возобновляю агентский цикл с раунда %d (повторный запуск с --resume)", agentLabel(step.Agent, step.Role), step.ID, rs.Rounds+1)
		genCtx = runner.WithResumeState(ctx, rs)
	}

	resp, err := e.provider.Generate(genCtx, agent)
	if err != nil {
		return err
	}

	// Цикл упёрся в лимит раундов (или модель обрезалась по лимиту токенов):
	// сохраняем историю диалога в чекпоинт, чтобы следующий запуск с --resume
	// продолжил шаг с раунда resp.Rounds+1, и останавливаем выполнение плана.
	if resp != nil && resp.Truncated {
		if e.persistRoundState(ctx, step, resp) {
			logging.Warnf("[%s] шаг %s: истощён лимит раундов (%d), история сохранена в чекпоинт", agentLabel(step.Agent, step.Role), step.ID, resp.Rounds)
		}
		if strings.TrimSpace(resp.Content) == "" {
			// Модель исчерпала лимит и вернула пустой ответ без вызовов
			// инструментов — код не создан. Считаем шаг упавшим, чтобы
			// чекпоинт пометил его failed и можно было повторить через resume.
			return fmt.Errorf("агент %T не создал код: модель вернула пустой ответ (исчерпан лимит раундов: %d)", agent, resp.Rounds)
		}
		if e.store != nil {
			// Чекпоинт подключён — останавливаем план: пользователь запустит
			// следующий запуск с --resume, и шаг продолжится с раунда
			// resp.Rounds+1 (например 13..24, затем снова resume — 25..36).
			return fmt.Errorf("шаг %q: исчерпан лимит раундов (%d) агентского цикла, история сохранена — запустите с --resume, чтобы продолжить", step.ID, resp.Rounds)
		}
		logging.Warnf("[%s] шаг %s: цикл исчерпал лимит раундов (%d), но чекпоинт отключён, продолжаю с частичным результатом", agentLabel(step.Agent, step.Role), step.ID, resp.Rounds)
	}

	return nil
}

// loadResumeState читает сохранённую историю агентского цикла шага из
// чекпоинта. Возвращает состояние для продолжения цикла и true, если
// история есть. Возвращает false при resume=false, без чекпоинта или если
// истории для шага не сохранялось.
func (e *Executor) loadResumeState(ctx context.Context, stepID string) (*runner.ResumeState, bool) {
	if e.store == nil || !e.resume {
		return nil, false
	}
	snap, err := e.store.Load(ctx)
	if err != nil {
		return nil, false
	}
	conv, ok := snap.Conversations[stepID]
	if !ok || len(conv) == 0 {
		return nil, false
	}
	var msgs []runner.Message
	if uerr := json.Unmarshal(conv, &msgs); uerr != nil {
		logging.Warnf("[Checkpoint] повреждена сохранённая история шага %s (%v), начинаю шаг заново", stepID, uerr)
		return nil, false
	}
	return &runner.ResumeState{Messages: msgs, Rounds: snap.Rounds[stepID]}, true
}

// persistRoundState сохраняет историю диалога и потраченные раунды шага в
// чекпоинт, чтобы при resume продолжить агентский цикл. Возвращает true,
// если состояние сохранено.
func (e *Executor) persistRoundState(ctx context.Context, step *Step, resp *runner.AgentResponse) bool {
	if e.store == nil || len(resp.Messages) == 0 {
		return false
	}
	conv, err := json.Marshal(resp.Messages)
	if err != nil {
		logging.Warnf("[Checkpoint] ошибка сериализации истории шага %s: %v", step.ID, err)
		return false
	}
	snap, err := e.store.Load(ctx)
	if err != nil {
		logging.Warnf("[Checkpoint] ошибка чтения чекпоинта для шага %s: %v", step.ID, err)
		return false
	}
	if err := e.store.SaveRoundState(ctx, snap, step.ID, resp.Rounds, conv); err != nil {
		logging.Warnf("[Checkpoint] ошибка сохранения истории шага %s: %v", step.ID, err)
		return false
	}
	return true
}

// runReviewAgent запускает локальное ревью проекта (LocalForge) и печатает
// найденные замечания. Не требует URL — работает с temp/<projectName>.
// Дифф строится только по файлам из области работы шага, чтобы ревьювер
// не смотрел на код, не затронутый работой.
//
// Если замечания найдены — включается цикл «ревью → исправление → ревью»
// (бюджет REVIEW_FIX_ROUNDS): планировщик составляет шаги исправления
// разработчиками backend/frontend, исполнитель их прогоняет, затем ревью
// повторяется по обновлённому коду. Цикл завершается, когда замечаний
// больше нет.
func (e *Executor) runReviewAgent(ctx context.Context, step *Step, projectName string) error {
	dir := projects.ProjectDir(projectName)
	cfg := codereviewer.LoadConfig()

	// Область ревью расширяется между раундами только файлами исправлений,
	// чтобы фиксы (вне исходного scope) тоже попадали в повторное ревью.
	reviewScope := step.Scope

	for round := 1; round <= cfg.MaxRounds; round++ {
		comments, err := e.runReviewRound(ctx, step, projectName, dir, reviewScope)
		if err != nil {
			return err
		}

		logging.Infof("[код-ревьювер] ревью %q (раунд %d/%d, scope: %v): найдено замечаний: %d",
			dir, round, cfg.MaxRounds, reviewScope, len(comments))

		if len(comments) == 0 {
			logging.Infof("[код-ревьювер] ревью %q: замечаний больше нет — ревью пройдено", dir)
			return nil
		}

		if round >= cfg.MaxRounds {
			return fmt.Errorf("ревью %q не пройдено после %d раунда(ов) — осталось замечаний: %d",
				dir, cfg.MaxRounds, len(comments))
		}

		// Замечания найдены: планируем шаги исправления и прогоняем их.
		fixes, err := e.planReviewFixes(ctx, dir, comments)
		if err != nil {
			return fmt.Errorf("раунд ревью %d: планирование исправлений: %w", round, err)
		}
		if len(fixes) == 0 {
			logging.Warnf("[планировщик] раунд ревью %d: планировщик не вернул шагов исправлений", round)
			continue
		}

		executed := 0
		for _, fs := range fixes {
			// Повторную приёмку и ревью запускают циклы исполнителя, а не
			// планировщик исправлений.
			if fs.Agent == AgentAcceptor || fs.Agent == AgentCodeReviewer {
				logging.Detailf("[%s] раунд ревью %d: шаг %s в плане исправлений пропущен", agentLabel(fs.Agent, fs.Role), round, fs.Agent)
				continue
			}
			fixStep := fs
			fixStep.ID = reviewFixStepID(e, round, executed)
			// Область исправления: если планировщик не указал scope —
			// подставляем файлы, на которые указывают замечания ревью.
			if len(fixStep.Scope) == 0 {
				fixStep.Scope = commentFiles(comments)
			}
			e.plan.Steps = append(e.plan.Steps, fixStep)
			logging.Infof("[%s] раунд ревью %d: шаг исправления %s: %s", agentLabel(fixStep.Agent, fixStep.Role), round, fixStep.ID, fixStep.Description)
			e.markRunning(ctx, fixStep.ID)
			if err := e.executeStep(ctx, &fixStep); err != nil {
				e.markFailed(ctx, fixStep.ID)
				return fmt.Errorf("раунд ревью %d, шаг исправления %q: %w", round, fixStep.ID, err)
			}
			e.markDone(ctx, fixStep.ID)
			executed++
			reviewScope = uniqueSlash(append(reviewScope, fixStep.Scope...))
		}
		if executed == 0 {
			logging.Warnf("[планировщик] раунд ревью %d: планировщик не дал применимых шагов исправлений — повторяю ревью", round)
			continue
		}

		logging.Infof("[код-ревьювер] раунд ревью %d: повторное ревью после исправлений", round)
	}

	return fmt.Errorf("ревью %q не пройдено после %d раундов исправлений", dir, cfg.MaxRounds)
}

// runReviewRound выполняет один проход локального ревью и возвращает
// опубликованные замечания (пустой слайс — замечаний нет).
func (e *Executor) runReviewRound(ctx context.Context, step *Step, projectName, dir string, scope []string) ([]forges.ReviewComment, error) {
	lf, err := forges.NewLocalForge(dir)
	if err != nil {
		return nil, fmt.Errorf("локальное ревью: %v", err)
	}
	lf.SetScope(scope)

	agent := codereviewer.NewCodereviewerWithForge(lf, step.Prompt)
	resp, err := e.provider.Generate(ctx, agent)
	if err != nil {
		return nil, err
	}

	if len(lf.Published) == 0 && resp != nil && resp.Content != "" {
		agent.PublishParsedReview(resp.Content)
	}

	return lf.Published, nil
}

// planReviewFixes отдаёт планировщику замечания ревью и получает шаги
// исправления. Формат — тот же JSON-план, но планировщику запрещено
// добавлять acceptor и codereviewer: повторные ревью/приёмку запускают
// циклы исполнителя.
func (e *Executor) planReviewFixes(ctx context.Context, dir string, comments []forges.ReviewComment) ([]Step, error) {
	issueFiles := uniqueSlash(commentFiles(comments))

	var b strings.Builder
	b.WriteString("Код-ревью выявило замечания к проекту. Директория: ")
	b.WriteString(dir)
	b.WriteString(".\n\nЗамечания:\n")
	for _, c := range comments {
		loc := strings.TrimSpace(c.FilePath)
		if c.Line > 0 {
			loc += ":" + strconv.Itoa(c.Line)
		}
		if loc != "" {
			fmt.Fprintf(&b, "- %s %s\n", loc, strings.TrimSpace(c.Text))
		} else {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(c.Text))
		}
	}

	b.WriteString(`
Составь план исправлений этих замечаний.
Требования:
- Все шаги — только агенты-разработчики backend или frontend, выбранные по принадлежности файлов к подпроекту (server/→backend, frontend/→frontend).
- НЕ добавляй шаги acceptor и codereviewer — повторные ревью и приёмку запустит исполнитель.
- scope каждого шага — ТОЛЬКО файлы, реально требующие правки, обязательно включая:
  `)
	if len(issueFiles) > 0 {
		b.WriteString(bullet(issueFiles))
	} else {
		b.WriteString("  (файлы из замечаний ревью)")
	}
	b.WriteString("\n- Каждый шаг — одно конкретное исправление.")

	pa := NewPlanner(filepath.Base(dir), b.String())
	resp, err := e.provider.Generate(ctx, pa)
	if err != nil {
		return nil, err
	}
	fixPlan, err := ParsePlan(resp.Content)
	if err != nil {
		logging.Warnf("[планировщик] не удалось разобрать план исправлений: %v", err)
		logging.Detailf("[планировщик] Ответ планировщика:\n%s", resp.Content)
		return nil, err
	}
	return fixPlan.Steps, nil
}

// commentFiles возвращает уникальные нормализованные файлы, на которые
// указывают замечания ревью.
func commentFiles(comments []forges.ReviewComment) []string {
	seen := map[string]bool{}
	var files []string
	for _, c := range comments {
		f := strings.TrimSpace(strings.TrimPrefix(c.FilePath, "./"))
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	return files
}

// reviewFixStepID генерирует уникальный ID шага исправления ревью, чтобы не
// пересекаться с ID шагов основного плана и других раундов.
func reviewFixStepID(e *Executor, round, idx int) string {
	base := fmt.Sprintf("review-r%d-%d", round, idx)
	if e.findStep(base) == nil {
		return base
	}
	for n := 1; ; n++ {
		id := fmt.Sprintf("review-r%d-%d-%d", round, idx, n)
		if e.findStep(id) == nil {
			return id
		}
	}
}

// runAcceptorAgent выполняет детерминированную приёмку собранного приложения:
// определяет тип проекта, собирает его, запускает на короткое время и
// анализирует логи. Сам шаг никогда не «падает» — отрицательный вердикт
// сохраняется в отчёт и обрабатывается циклом runAcceptanceLoop.
//
// Если scope шага указывает на один подкаталог (например frontend/ или
// server/) — приёмка выполняется в нём отдельно: у фронтенда и бэкенда
// собственная сборка и проверки. Пустой scope — приёмка всего корня
// (acceptor сам найдёт подпроекты и примет каждый по отдельности).
func (e *Executor) runAcceptorAgent(ctx context.Context, step *Step, projectName string) error {
	root := projects.ProjectDir(projectName)
	dir := acceptanceDir(root, step.Scope)
	rep := acceptor.Accept(dir, acceptor.LoadConfig())
	e.acceptReports[step.ID] = rep

	if rep.Verdict == acceptor.VerdictApprove {
		logging.Infof("[приёмка] шаг %s: приёмка %q пройдена (%s)", step.ID, dir, rep.Summary)
	} else {
		logging.Infof("[приёмка] шаг %s: приёмка %q НЕ пройдена (%s)", step.ID, dir, rep.Summary)
		for _, iss := range rep.Issues {
			loc := iss.File
			if iss.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, iss.Line)
			}
			if loc != "" {
				logging.Detailf("[приёмка]   - [%s] %s %s", iss.Severity, loc, iss.Text)
			} else {
				logging.Detailf("[приёмка]   - [%s] %s", iss.Severity, iss.Text)
			}
		}
	}
	return nil
}

// acceptanceDir выбирает директорию приёмки для шага. Если scope шага сужается
// до одного существующего подкаталога (например ["frontend/"]) — приёмка идёт
// в нём; иначе — в корне проекта.
func acceptanceDir(root string, scope []string) string {
	top := topLevelScopeDir(scope)
	if top == "" {
		return root
	}
	candidate := filepath.Join(root, top)
	if st, err := os.Stat(candidate); err == nil && st.IsDir() {
		return candidate
	}
	return root
}

// topLevelScopeDir возвращает общую старшую директорию всех записей scope,
// если они все лежат в одном подкаталоге проекта ("frontend" для
// ["frontend/src/App.tsx", "frontend/package.json"]). Возвращает "" при разных
// подкаталогах, пустом scope или файле в корне.
func topLevelScopeDir(scope []string) string {
	seen := map[string]bool{}
	var dirs []string
	for _, s := range scope {
		s = strings.TrimPrefix(strings.TrimSpace(strings.ReplaceAll(s, "\\", "/")), "./")
		s = strings.Trim(strings.TrimSpace(s), "/")
		if s == "" {
			continue
		}
		first := s
		if i := strings.IndexByte(s, '/'); i >= 0 {
			first = s[:i]
		}
		if first == "." || first == ".." {
			continue
		}
		if !seen[first] {
			seen[first] = true
			dirs = append(dirs, first)
		}
	}
	if len(dirs) == 1 {
		return dirs[0]
	}
	return ""
}

// runAcceptanceLoop — цикл «приёмка → планировщик исправлений → приёмка».
// Пока хотя бы один шаг acceptor плана не прошёл приёмку (и не исчерпан
// бюджет раундов ACCEPT_MAX_ROUNDS): отчёт приёмки передаётся планировщику,
// тот составляет шаги исправления разработчиками backend/frontend,
// исполнитель их прогоняет и повторяет приёмку.
func (e *Executor) runAcceptanceLoop(ctx context.Context) error {
	cfg := acceptor.LoadConfig()
	if cfg.MaxRounds <= 0 {
		return nil
	}

	// Шаги приёмки плана. Исправления выполняются сразу, без добавления
	// в волны: план остаётся неизменным.
	var acceptSteps []*Step
	for i := range e.plan.Steps {
		if e.plan.Steps[i].Agent == AgentAcceptor {
			acceptSteps = append(acceptSteps, &e.plan.Steps[i])
		}
	}
	if len(acceptSteps) == 0 {
		return nil
	}

	for round := 1; round <= cfg.MaxRounds; round++ {
		var failing []*Step
		for _, s := range acceptSteps {
			switch rep := e.acceptReports[s.ID]; {
			case rep == nil:
				failing = append(failing, s)
			case rep.Verdict != acceptor.VerdictApprove:
				failing = append(failing, s)
			}
		}
		if len(failing) == 0 {
			logging.Infof("[приёмка] приёмка пройдена: все проекты соответствуют требованиям")
			return nil
		}

		for _, s := range failing {
			rep := e.acceptReports[s.ID]
			// Check if report is nil before proceeding with fixes
			if rep == nil {
				logging.Warnf("[приёмка] раунд %d/%d: отчёт приёмки отсутствует для шага %q", round, cfg.MaxRounds, s.Description)
				// Try to re-run acceptor for this step
				logging.Infof("[приёмка] раунд %d/%d: повторный запуск приёмки для шага %q", round, cfg.MaxRounds, s.Description)
				if err := e.runAcceptorAgent(ctx, s, e.plan.ProjectName); err != nil {
					return fmt.Errorf("раунд приёмки %d: повторный запуск приёмки для шага %q: %w", round, s.Description, err)
				}
				// Check again if the report is now available
				rep = e.acceptReports[s.ID]
				if rep == nil {
					return fmt.Errorf("раунд приёмки %d: отчёт приёмки всё ещё отсутствует для шага %q", round, s.Description)
				}
			}

			logging.Infof("[приёмка] раунд %d/%d: приёмка %q не пройдена — вызываю планировщик исправлений", round, cfg.MaxRounds, s.Description)

			fixes, err := e.planFixSteps(ctx, rep)
			if err != nil {
				return fmt.Errorf("раунд приёмки %d: планирование исправлений: %w", round, err)
			}
			if len(fixes) == 0 {
				logging.Warnf("[приёмка] раунд %d: планировщик не вернул шагов исправлений", round)
				return fmt.Errorf("раунд приёмки %d: планировщик не составил план исправлений для %q", round, s.Description)
			}

			// Выполняем шаги исправления (planning-результат), пропуская
			// неприменимые/лишние типы агентов.
			executed := 0
			for _, fs := range fixes {
				if fs.Agent == AgentAcceptor {
					// Модель проигнорировала запрет: приёмку запускает цикл ниже.
					logging.Detailf("[приёмка] раунд %d: шаг acceptor в плане исправлений пропущен", round)
					continue
				}
				step := fs
				step.ID = fixStepID(e, round, executed)
				// Обновлённая область видимости: если планировщик не указал
				// scope (или указал только «мусорный» узкий), подставляем
				// файлы из отчёта приёмки, чтобы фикс-агента не заблокировала
				// запись нужных исходников.
				if len(step.Scope) == 0 {
					step.Scope = rep.IssueFiles()
				}
				// Переланировка добавляет задачу исправления в план: она
				// становится частью плана (findStep/чекпоинт/resume), а её
				// scope — обновлённый (файлы из отчёта приёмки).
				e.plan.Steps = append(e.plan.Steps, step)
				logging.Infof("[%s] раунд %d: шаг исправления %s: %s", agentLabel(step.Agent, step.Role), round, step.ID, step.Description)
				e.markRunning(ctx, step.ID)
				if err := e.executeStep(ctx, &step); err != nil {
					e.markFailed(ctx, step.ID)
					return fmt.Errorf("раунд приёмки %d, шаг исправления %q: %w", round, step.ID, err)
				}
				e.markDone(ctx, step.ID)
				executed++
			}
			if executed == 0 {
				return fmt.Errorf("раунд приёмки %d: планировщик не дал применимых шагов исправлений", round)
			}

			// Повторная приёмка после исправлений.
			logging.Infof("[приёмка] раунд %d: повторная приёмка после исправлений", round)
			if err := e.runAcceptorAgent(ctx, s, e.plan.ProjectName); err != nil {
				return err
			}
		}
	}

	logging.Warnf("[приёмка] исчерпан бюджет раундов приёмки (%d) — остались неисправленные замечания", cfg.MaxRounds)
	return fmt.Errorf("приёмка не пройдена после %d раундов исправлений, см. лог приёмки", cfg.MaxRounds)
}

// planFixSteps отдаёт планировщику отчёт приёмки и получает шаги исправления.
// Формат — тот же JSON-план, но планировщику запрещено добавлять acceptor:
// повторную приёмку запускает цикл приёмки исполнителя.
func (e *Executor) planFixSteps(ctx context.Context, rep *acceptor.Report) ([]Step, error) {
	// Обновлённая область видимости для задач исправления: исходит из тех
	// файлов, на которые реально указывает приёмка (acceptor не ограничен
	// областью видимости и видит весь модуль). Эти локации передаём
	// планировщику, чтобы scope шага покрывал файлы, которые придётся менять,
	// а не сузился до произвольного (например, [go.mod]) и не заблокировал
	// фикс-агента на записи нужных файлов.
	issueFiles := []string{}
	if rep != nil {
		issueFiles = uniqueSlash(rep.IssueFiles())
	}
	prompt := fmt.Sprintf(`Приёмка собранного приложения не пройдена.

%s

Составь план исправлений этих ошибок.
Требования:
- Все шаги — только агенты-разработчики backend или frontend, выбранные по принадлежности файлов к подпроекту (server/→backend, frontend/→frontend) (для проверки исправлений можно добавить codereviewer).
- НЕ добавляй шаг acceptor — повторную приёмку запустит исполнитель.
- scope каждого шага — ТОЛЬКО файлы, реально требующие правки, обязательно включая:
%s
  (а не весь проект и не узкую догадку вроде только [go.mod], если правка нужна в исходниках).
- Каждый шаг — одно конкретное исправление.`, rep.FixPrompt(), bullet(issueFiles))

	pa := NewPlanner(rep.Project, prompt)
	resp, err := e.provider.Generate(ctx, pa)
	if err != nil {
		return nil, err
	}
	fixPlan, err := ParsePlan(resp.Content)
	if err != nil {
		logging.Warnf("[планировщик] не удалось разобрать план исправлений: %v", err)
		logging.Detailf("[планировщик] Ответ планировщика:\n%s", resp.Content)
		return nil, err
	}
	return fixPlan.Steps, nil
}

// fixStepID генерирует уникальный ID шага исправления приёмки, чтобы не
// пересекаться с ID шагов основного плана и других раундов.
func fixStepID(e *Executor, round, idx int) string {
	base := fmt.Sprintf("accept-r%d-%d", round, idx)
	if e.findStep(base) == nil {
		return base
	}
	for n := 1; ; n++ {
		id := fmt.Sprintf("accept-r%d-%d-%d", round, idx, n)
		if e.findStep(id) == nil {
			return id
		}
	}
}

// uniqueSlash возвращает уникальные элементы списка, нормализуя пути к
// slash-формату.
func uniqueSlash(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		it = strings.TrimSpace(strings.ReplaceAll(it, "\\", "/"))
		if it == "" || seen[it] {
			continue
		}
		seen[it] = true
		out = append(out, it)
	}
	return out
}

// bullet форматирует список как маркированный отступ (пустой список → «—»).
func bullet(items []string) string {
	if len(items) == 0 {
		return "  - (файлы из отчёта приёмки)"
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString("  - ")
		b.WriteString(it)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// findStep находит шаг по ID.
func (e *Executor) findStep(id string) *Step {
	for i := range e.plan.Steps {
		if e.plan.Steps[i].ID == id {
			return &e.plan.Steps[i]
		}
	}
	return nil
}
