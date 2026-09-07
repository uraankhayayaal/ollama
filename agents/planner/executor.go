package planner

import (
	"ai/agents"
	"ai/agents/acceptor"
	"ai/agents/codegenerator"
	"ai/agents/codereviewer"
	"ai/agents/refactor"
	"ai/checkpoint"
	"ai/forges"
	"ai/models"
	"ai/runner"
	"context"
	"encoding/json"
	"fmt"
	"log"
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
				log.Printf("[Plan] шаг %s уже выполнен ранее, пропускаю (resume)", stepID)
				continue
			}

			log.Printf("[Plan] шаг %s: %s (агент: %s)", step.ID, step.Description, step.Agent)
			e.markRunning(ctx, stepID)
			if err := e.executeStep(ctx, step); err != nil {
				e.markFailed(ctx, stepID)
				return fmt.Errorf("шаг %q: %w", step.ID, err)
			}
			e.markDone(ctx, stepID)
			log.Printf("[Plan] шаг %s завершён", step.ID)
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
			log.Printf("[Checkpoint] resume: восстановлено %d завершённых шагов", n)
			return nil
		} else if err != nil && err != checkpoint.ErrNotFound {
			log.Printf("[Checkpoint] не удалось прочитать чекпоинт (%v), начинаю заново", err)
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
						log.Printf("[Checkpoint] ошибка очистки истории шага %s: %v", stepID, err)
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
			log.Printf("[Checkpoint] ошибка сохранения статуса %s=%s: %v", stepID, status, err)
		}
	} else {
		log.Printf("[Checkpoint] ошибка чтения снапшота для %s: %v", stepID, err)
	}
}

// executeStep выполняет один шаг плана нужным агентом.
func (e *Executor) executeStep(ctx context.Context, step *Step) error {
	projectName := step.ProjectName
	if projectName == "" {
		projectName = e.plan.ProjectName
	}

	switch step.Agent {
	case AgentCodeGenerator, AgentRefactor:
		return e.runCodingAgent(ctx, step, projectName)

	case AgentCodeReviewer:
		return e.runReviewAgent(ctx, step, projectName)

	case AgentAcceptor:
		return e.runAcceptorAgent(ctx, step, projectName)

	default:
		return fmt.Errorf("неизвестный тип агента: %s", step.Agent)
	}
}

// runCodingAgent запускает генератор или рефактор с последующим self-review
// (если включено конфигом). Оба агента ограничены областью работы scope:
// пишут/читают только указанные файлы, и ревью тоже идёт только по ним.
// refactor встраивает codegenerator, поэтому интерфейс selfReviewer
// доступен обоим.
func (e *Executor) runCodingAgent(ctx context.Context, step *Step, projectName string) error {
	var agent agents.Agent
	switch step.Agent {
	case AgentCodeGenerator:
		agent = codegenerator.NewCodegenerator(projectName, step.Prompt)
	case AgentRefactor:
		ra, err := refactor.NewRefactorAgent(step.Prompt, projectName)
		if err != nil {
			return err
		}
		agent = ra
	}

	// Ограничиваем инструменты агента областью работы шага: вне scope он не
	// сможет ни читать, ни писать, ни удалять файлы.
	if scoper, ok := agent.(interface{ SetScope([]string) }); ok {
		scoper.SetScope(step.Scope)
	}

	// Возобновление агентского цикла: если в чекпоинте сохранена история
	// диалога (в прошлом запуске шаг упёрся в лимит раундов), передаём её
	// в цикл, чтобы продолжить с места остановки, а не начинать заново.
	// Контекст с resume-состоянием используется ТОЛЬКО для этого вызова,
	// чтобы self-review ниже не подхватил чужую историю.
	genCtx := ctx
	if rs, ok := e.loadResumeState(ctx, step.ID); ok {
		log.Printf("[Plan] шаг %s: возобновляю агентский цикл с раунда %d (повторный запуск с --resume)", step.ID, rs.Rounds+1)
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
			log.Printf("[Plan] шаг %s: истощён лимит раундов (%d), история сохранена в чекпоинт", step.ID, resp.Rounds)
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
		log.Printf("[Plan] шаг %s: цикл исчерпал лимит раундов (%d), но чекпоинт отключён, продолжаю с частичным результатом", step.ID, resp.Rounds)
	}

	// Self-review сгенерированного/рефакторенного кода — тоже только по
	// файлам из области работы шага.
	if sr, ok := agent.(selfReviewer); ok {
		runRepairLoop(ctx, e.provider, sr, step.Scope, step.Prompt)
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
		log.Printf("[Checkpoint] повреждена сохранённая история шага %s (%v), начинаю шаг заново", stepID, uerr)
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
		log.Printf("[Checkpoint] ошибка сериализации истории шага %s: %v", step.ID, err)
		return false
	}
	snap, err := e.store.Load(ctx)
	if err != nil {
		log.Printf("[Checkpoint] ошибка чтения чекпоинта для шага %s: %v", step.ID, err)
		return false
	}
	if err := e.store.SaveRoundState(ctx, snap, step.ID, resp.Rounds, conv); err != nil {
		log.Printf("[Checkpoint] ошибка сохранения истории шага %s: %v", step.ID, err)
		return false
	}
	return true
}

// runReviewAgent запускает локальное ревью проекта (LocalForge) и печатает
// найденные замечания. Не требует URL — работает с temp/<projectName>.
// Дифф строится только по файлам из области работы шага, чтобы ревьювер
// не смотрел на код, не затронутый работой.
func (e *Executor) runReviewAgent(ctx context.Context, step *Step, projectName string) error {
	dir := codegenerator.ProjectDir(projectName)
	lf, err := forges.NewLocalForge(dir)
	if err != nil {
		return fmt.Errorf("локальное ревью: %v", err)
	}
	lf.SetScope(step.Scope)

	agent := codereviewer.NewCodereviewerWithForge(lf, step.Prompt)
	resp, err := e.provider.Generate(ctx, agent)
	if err != nil {
		return err
	}

	if len(lf.Published) == 0 && resp != nil && resp.Content != "" {
		agent.PublishParsedReview(resp.Content)
	}

	log.Printf("[Plan] ревью %q (scope: %v): найдено замечаний: %d", dir, step.Scope, len(lf.Published))
	return nil
}

// runAcceptorAgent выполняет детерминированную приёмку собранного приложения:
// определяет тип проекта, собирает его, запускает на короткое время и
// анализирует логи. Сам шаг никогда не «падает» — отрицательный вердикт
// сохраняется в отчёт и обрабатывается циклом runAcceptanceLoop.
func (e *Executor) runAcceptorAgent(ctx context.Context, step *Step, projectName string) error {
	dir := codegenerator.ProjectDir(projectName)
	rep := acceptor.Accept(dir, acceptor.LoadConfig())
	e.acceptReports[step.ID] = rep

	if rep.Verdict == acceptor.VerdictApprove {
		log.Printf("[Accept] шаг %s: приёмка %q пройдена (%s)", step.ID, dir, rep.Summary)
	} else {
		log.Printf("[Accept] шаг %s: приёмка %q НЕ пройдена (%s)", step.ID, dir, rep.Summary)
		for _, iss := range rep.Issues {
			loc := iss.File
			if iss.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, iss.Line)
			}
			if loc != "" {
				log.Printf("[Accept]   - [%s] %s %s", iss.Severity, loc, iss.Text)
			} else {
				log.Printf("[Accept]   - [%s] %s", iss.Severity, iss.Text)
			}
		}
	}
	return nil
}

// runAcceptanceLoop — цикл «приёмка → планировщик исправлений → приёмка».
// Пока хотя бы один шаг acceptor плана не прошёл приёмку (и не исчерпан
// бюджет раундов ACCEPT_MAX_ROUNDS): отчёт приёмки передаётся планировщику,
// тот составляет шаги исправления (refactor), исполнитель их прогоняет и
// повторяет приёмку.
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
			log.Printf("[Accept] приёмка пройдена: все проекты соответствуют требованиям")
			return nil
		}

		for _, s := range failing {
			rep := e.acceptReports[s.ID]
			log.Printf("[Accept] раунд %d/%d: приёмка %q не пройдена — вызываю планировщик исправлений", round, cfg.MaxRounds, s.Description)

			fixes, err := e.planFixSteps(ctx, rep)
			if err != nil {
				return fmt.Errorf("раунд приёмки %d: планирование исправлений: %w", round, err)
			}
			if len(fixes) == 0 {
				log.Printf("[Accept] раунд %d: планировщик не вернул шагов исправлений", round)
				return fmt.Errorf("раунд приёмки %d: планировщик не составил план исправлений для %q", round, s.Description)
			}

			// Выполняем шаги исправления (planning-результат), пропуская
			// неприменимые/лишние типы агентов.
			executed := 0
			for _, fs := range fixes {
				if fs.Agent == AgentAcceptor {
					// Модель проигнорировала запрет: приёмку запускает цикл ниже.
					log.Printf("[Accept] раунд %d: шаг acceptor в плане исправлений пропущен", round)
					continue
				}
				step := fs
				step.ID = fixStepID(e, round, executed)
				log.Printf("[Accept] раунд %d: шаг исправления %s: %s (агент: %s)", round, step.ID, step.Description, step.Agent)
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
			log.Printf("[Accept] раунд %d: повторная приёмка после исправлений", round)
			if err := e.runAcceptorAgent(ctx, s, e.plan.ProjectName); err != nil {
				return err
			}
		}
	}

	log.Printf("[Accept] исчерпан бюджет раундов приёмки (%d) — остались неисправленные замечания", cfg.MaxRounds)
	return fmt.Errorf("приёмка не пройдена после %d раундов исправлений, см. лог [Accept]", cfg.MaxRounds)
}

// planFixSteps отдаёт планировщику отчёт приёмки и получает шаги исправления.
// Формат — тот же JSON-план, но планировщику запрещено добавлять acceptor:
// повторную приёмку запускает цикл приёмки исполнителя.
func (e *Executor) planFixSteps(ctx context.Context, rep *acceptor.Report) ([]Step, error) {
	prompt := fmt.Sprintf(`Приёмка собранного приложения не пройдена.

%s

Составь план исправлений этих ошибок.
Требования:
- Все шаги — только refactor (для проверки исправлений можно добавить codereviewer).
- НЕ добавляй шаг acceptor — повторную приёмку запустит исполнитель.
- scope каждого шага — только файлы, относящиеся к ошибкам приёмки, а не весь проект.
- Каждый шаг — одно конкретное исправление.`, rep.FixPrompt())

	pa := NewPlanner(rep.Project, prompt)
	resp, err := e.provider.Generate(ctx, pa)
	if err != nil {
		return nil, err
	}
	fixPlan, err := ParsePlan(resp.Content)
	if err != nil {
		log.Printf("[Accept] не удалось разобрать план исправлений: %v\nОтвет планировщика:\n%s", err, resp.Content)
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

// findStep находит шаг по ID.
func (e *Executor) findStep(id string) *Step {
	for i := range e.plan.Steps {
		if e.plan.Steps[i].ID == id {
			return &e.plan.Steps[i]
		}
	}
	return nil
}

// selfReviewer — интерфейс агента с self-review (без циклического импорта
// из main): SelfReviewDir + NewReviewAgentFor + FixPromptFor.
type selfReviewer interface {
	SelfReviewDir() string
	NewReviewAgentFor(dir string, focus string) (agents.Agent, forges.Forge, error)
	FixPromptFor(original string, comments []forges.ReviewComment) string
}

// runRepairLoop — цикл исправления кода по замечаниям локального ревью.
// Аналогичен doSelfReview в main.go, но живёт в пакете планировщика.
// И ревью, и исправления ограничены областью работ scope: замечания
// собираются только по файлам шага, а фиксы не трогают остальной код.
func runRepairLoop(ctx context.Context, provider models.LLMProvider, sr selfReviewer, scope []string, originalPrompt string) {
	dir := sr.SelfReviewDir()
	if dir == "" {
		return
	}

	maxRounds := codegenerator.LoadConfig().MaxRepairRounds
	if maxRounds <= 0 {
		return
	}

	reviewAgent, forge, err := sr.NewReviewAgentFor(dir, "")
	if err != nil {
		log.Printf("[Plan] self-review: не удалось создать агента ревью: %v", err)
		return
	}
	applyForgeScope(forge, scope)

	if _, err := provider.Generate(ctx, reviewAgent); err != nil {
		log.Printf("[Plan] self-review: ошибка ревью: %v", err)
		return
	}

	lf, ok := forge.(*forges.LocalForge)
	if !ok || len(lf.Published) == 0 {
		return
	}

	pending := lf.Published
	// Детект отсутствия прогресса: если замечания повторного ревью приходятся
	// на те же места, что и до исправления (та же сигнатура file:line), фикс
	// не помог — прерываем цикл, а не крутимся до исчерпания бюджета.
	prevSig := ""
	stuckRounds := 0
	for round := 1; round <= maxRounds && len(pending) > 0; round++ {
		log.Printf("[Plan] self-review: раунд %d/%d, замечаний: %d", round, maxRounds, len(pending))

		fixPrompt := sr.FixPromptFor(originalPrompt, pending)
		fixAgent, ferr := codegenerator.NewCodegeneratorInDir(fixPrompt, dir)
		if ferr != nil {
			log.Printf("[Plan] self-review: ошибка создания агента исправления: %v", ferr)
			break
		}
		// Фикс тоже работает только в рамках области шага.
		fixAgent.SetScope(scope)

		if _, err := provider.Generate(ctx, fixAgent); err != nil {
			log.Printf("[Plan] self-review: ошибка на этапе исправления: %v", err)
			break
		}
		fixAgent.Finalize()

		if round >= maxRounds {
			break
		}

		// Перечитываем код после правок и смотрим, остались ли замечания.
		newAgent, newForge, rerr := sr.NewReviewAgentFor(dir, "")
		if rerr != nil {
			log.Printf("[Plan] self-review: ошибка повторного ревью: %v", rerr)
			break
		}
		applyForgeScope(newForge, scope)
		if _, err := provider.Generate(ctx, newAgent); err != nil {
			log.Printf("[Plan] self-review: ошибка повторного ревью: %v", err)
			break
		}
		rl, ok2 := newForge.(*forges.LocalForge)
		if !ok2 {
			break
		}

		// Проверяем продвижение: не застряли ли мы на тех же местах.
		sig := forges.CommentSignature(rl.Published)
		if sig != "" && sig == prevSig {
			stuckRounds++
			if stuckRounds >= 2 {
				log.Printf("[Plan] self-review: исправление не продвигается (%d раунда(ов) те же места), прерываю цикл", stuckRounds)
				pending = rl.Published
				break
			}
		} else {
			stuckRounds = 0
		}
		prevSig = sig
		pending = rl.Published
	}

	if len(pending) > 0 {
		log.Printf("[Plan] self-review: завершён, осталось замечаний: %d", len(pending))
	} else {
		log.Printf("[Plan] self-review: завершён, замечаний больше нет")
	}
}

// applyForgeScope ограничивает локальный фордж ревью областью работ.
func applyForgeScope(f forges.Forge, scope []string) {
	if lf, ok := f.(*forges.LocalForge); ok {
		lf.SetScope(scope)
	}
}
