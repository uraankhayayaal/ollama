package planner

import (
	"ai/agents"
	"ai/agents/acceptor"
	"ai/agents/backendlead"
	"ai/agents/developer"
	"ai/agents/devops"
	"ai/agents/devopslead"
	"ai/agents/frontendlead"
	"ai/agents/qaengineer"
	"ai/agents/qalead"
	"ai/board"
	"ai/checkpoint"
	"ai/logging"
	"ai/models"
	"ai/projects"
	"ai/runner"
	"ai/tools"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ollama/ollama/api"
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
	case AgentBackendDev, AgentFrontendDev, AgentDevops:
		_, err := e.runCodingAgent(ctx, step, projectName)
		return err

	case AgentDevopsLead, AgentQALead, AgentFrontendLead, AgentBackendLead:
		return e.runLeadStep(ctx, step, projectName)

	case AgentQAEngineer:
		// Объединённый агент QA: сборка → автотесты → приёмка (детерминированная).
		return e.runAcceptorAgent(ctx, step, projectName)

	default:
		return fmt.Errorf("неизвестный тип агента: %s", step.Agent)
	}
}

// newStepAgent создаёт агента шага по его типу (разработчик, специалист или
// лид направления). Возвращает nil для неизвестного типа.
func newStepAgent(step *Step, projectName string) agents.Agent {
	switch step.Agent {
	case AgentBackendDev:
		return developer.NewBackendDeveloper(projectName, step.Prompt)
	case AgentFrontendDev:
		return developer.NewFrontendDeveloper(projectName, step.Prompt)
	case AgentDevops:
		return devops.NewDevops(projectName, step.Prompt)
	case AgentDevopsLead:
		return devopslead.NewDevopsLead(projectName, step.Prompt)
	case AgentQAEngineer:
		return qaengineer.NewQAEngineer(projectName, step.Prompt)
	case AgentQALead:
		return qalead.NewQALead(projectName, step.Prompt)
	case AgentFrontendLead:
		return frontendlead.NewFrontendLead(projectName, step.Prompt)
	case AgentBackendLead:
		return backendlead.NewBackendLead(projectName, step.Prompt)
	}
	return nil
}

// runCodingAgent запускает агента-разработчика (backend/frontend) или другого
// специалиста (devops, qa, лиды). Все агенты ограничены областью работы scope:
// пишут/читают только указанные файлы. Возвращает ответ модели (нужен циклу
// QA-багрепортов: observable из него парсятся репорты дефектов).
func (e *Executor) runCodingAgent(ctx context.Context, step *Step, projectName string) (*runner.AgentResponse, error) {
	agent := newStepAgent(step, projectName)
	if agent == nil {
		return nil, fmt.Errorf("неизвестный тип агента: %s", step.Agent)
	}

	// Ограничиваем инструменты агента областью работы шага: вне scope он не
	// сможет ни читать, ни писать, ни удалять файлы. Для шагов создания scope
	// предварительно расширяется до директорий (см. effectiveCreationScope).
	scope := effectiveCreationScope(projects.ProjectDir(projectName), step.Scope)
	if len(scope) != len(step.Scope) {
		logging.Detailf("[%s] шаг %s: scope шага расширен для создания: %v -> %v", agentLabel(step.Agent, step.Role), step.ID, step.Scope, scope)
	}
	if scoper, ok := agent.(interface{ SetScope([]string) }); ok {
		scoper.SetScope(scope)
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
		return nil, err
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
			return nil, fmt.Errorf("агент %T не создал код: модель вернула пустой ответ (исчерпан лимит раундов: %d)", agent, resp.Rounds)
		}
		if e.store != nil {
			// Чекпоинт подключён — останавливаем план: пользователь запустит
			// следующий запуск с --resume, и шаг продолжится с раунда
			// resp.Rounds+1 (например 13..24, затем снова resume — 25..36).
			return nil, fmt.Errorf("шаг %q: исчерпан лимит раундов (%d) агентского цикла, история сохранена — запустите с --resume, чтобы продолжить", step.ID, resp.Rounds)
		}
		logging.Warnf("[%s] шаг %s: цикл исчерпал лимит раундов (%d), но чекпоинт отключён, продолжаю с частичным результатом", agentLabel(step.Agent, step.Role), step.ID, resp.Rounds)
	}

	return resp, nil
}

// runLeadStep выполняет шаг-декомпозицию лида направления и сразу реализует
// её: лид возвращает JSON-декомпозицию эпика на задачи (в plan-режиме доска
// не подключена, поэтому публикация необязательна), исполнитель печатает в
// консоль «Список задач лида» и выполняет каждую задачу агентом-специалистом
// (QA/DevOps или разработчик backend/frontend) строго в рамках scope шага.
// Так план, делегирующий разработку лидам, снова реально создаёт файлы.
func (e *Executor) runLeadStep(ctx context.Context, step *Step, projectName string) error {
	lead := newStepAgent(step, projectName)
	if lead == nil {
		return fmt.Errorf("неизвестный тип агента-лида: %s", step.Agent)
	}

	// Лид scope НЕ ограничиваем: он только читает проект (List/ReadFiles) и
	// ведёт декомпозицию, пишущих инструментов у него нет. Сужать чтение до
	// step.Scope вредно — лиду нужно видеть проект целиком (в т.ч. корневой
	// README, соседние подпроекты монорепозитория), а «файл вне области
	// работы» на чтении лишь зацикливало шаг на ошибках. Специалистам, которые
	// выполняют задачи декомпозиции, scope ниже всё равно устанавливается.
	if rr, ok := lead.(interface{ SetRole(string) }); ok && step.Role != "" {
		rr.SetRole(string(step.Role))
	}

	// Шаг лида атомарен (декомпозиция + реализация): сохранённую историю
	// прошлого цикла отбрасываем, чтобы при resume шаг выполнился целиком,
	// а история лида не подменилась историей специалиста.
	if _, ok := e.loadResumeState(ctx, step.ID); ok {
		logging.Infof("[%s] шаг %s: сбрасываю устаревшую историю resume, шаг выполнится заново", agentLabel(step.Agent, step.Role), step.ID)
	}
	if e.store != nil {
		if snap, err := e.store.Load(ctx); err == nil {
			if _, ok := snap.Conversations[step.ID]; ok {
				_ = e.store.ClearRoundState(ctx, snap, step.ID)
			}
		}
	}

	resp, err := e.provider.Generate(ctx, lead)
	if err != nil {
		return fmt.Errorf("декомпозиция эпика %q: %w", step.ID, err)
	}
	if resp != nil && resp.Truncated {
		if err := e.truncatedStepError(ctx, step, resp); err != nil {
			return err
		}
	}

	tasks, err := board.UnmarshalTasks(resp.Content)
	if (err != nil || len(tasks) == 0) && !resp.Truncated {
		// Модель вместо JSON-декомпозиции вернула текст (пути, рассуждения): 
		// не фаталим сразу — один раз спрашиваем ещё раз с чёткой инструкцией.
		// Повторный ответ обрабатываем как основной (петля «переспросить»
		// ограничена ровно одной попыткой, чтобы не растить токен-бюджет).
		logging.Warnf("[%s] шаг %s: лид не вернул JSON-декомпозицию (%v), переспрашиваю один раз",
			agentLabel(step.Agent, step.Role), step.ID, err)
		if retryResp, rerr := e.provider.Generate(ctx, leadWithHint(lead, resp.Content)); rerr == nil {
			resp = retryResp
			tasks, err = board.UnmarshalTasks(resp.Content)
		}
	}
	if err != nil || len(tasks) == 0 {
		return fmt.Errorf("лид не вернул JSON-декомпозицию задач (задач: %d, ошибка: %v)", len(tasks), err)
	}
	printDecomposedTasks(tasks)
	sortTaskSpecs(tasks)

	// Раздел «План работ» в README: лид уже задокументировал свой план
	// (промптом), а оркестратор детерминированно ведёт статусы каждой задачи,
	// чтобы следующий разработчик знал этап и объём реализованных работ.
	doneTasks := map[string]bool{}
	if err := e.syncPlanReadme(projectName, step.ID, tasks, doneTasks); err != nil {
		logging.Warnf("[%s] шаг %s: не удалось записать план работ в README: %v", agentLabel(step.Agent, step.Role), step.ID, err)
	}

	for _, ts := range tasks {
		spec := specialistForRole(projectName, ts.AssignedRole, leadTaskPrompt(projectName, ts))
		// Специалисты, в отличие от лида, пишут код: ограничиваем их корневой
		// директорией шага (leadStepScope), а не точечным scope из плана —
		// планировщик указывает конкретные новые файлы (например,
		// frontend/package.json), а лид декомпозирует шаг на задачи, которые
		// трогают произвольные файлы всей области (компоненты, стили, сервисы).
		if scoper, ok := spec.(interface{ SetScope([]string) }); ok {
			scoper.SetScope(leadStepScope(projects.ProjectDir(projectName), step))
		}
		logging.Infof("[задача %s] специалист %s выполняет: %s", ts.TaskID, truncateText(ts.AssignedRole, 30), truncateText(ts.Title, 60))
		resp, err := e.provider.Generate(ctx, spec)
		if err != nil {
			return fmt.Errorf("шаг %q, задача %s: %w", step.ID, ts.TaskID, err)
		}
		if resp != nil && resp.Truncated {
			if err := e.truncatedStepError(ctx, step, resp); err != nil {
				return err
			}
		}
		doneTasks[ts.TaskID] = true
		if err := e.syncPlanReadme(projectName, step.ID, tasks, doneTasks); err != nil {
			logging.Warnf("[%s] шаг %s: не удалось обновить статус задачи %s в README: %v", agentLabel(step.Agent, step.Role), step.ID, ts.TaskID, err)
		}
		logging.Infof("[задача %s] выполнена специалистом %s", ts.TaskID, truncateText(ts.AssignedRole, 30))
	}
	return nil
}

// syncPlanReadme детерминированно ведёт раздел «План работ» в README проекта:
// список задач декомпозиции лида с их статусами и прогрессом. Раздел
// ограничен HTML-комментариями-маркерами и перезаписывается целиком, поэтому
// оркестратор не конфликтует с текстом, который лиды/разработчики добавляют
// в README. doneTasks — множество task_id, уже выполненных специалистами.
func (e *Executor) syncPlanReadme(projectName, stepID string, tasks []board.TaskSpec, doneTasks map[string]bool) error {
	projectDir := projects.ProjectDir(projectName)
	readme := filepath.Join(projectDir, readmePlanName(projectDir))

	openMarker := "<!-- plan-status:" + stepID + " -->"
	closeMarker := "<!-- /plan-status:" + stepID + " -->"

	var body strings.Builder
	doneCount, total := 0, len(tasks)
	for _, t := range tasks {
		if doneTasks[t.TaskID] {
			doneCount++
		}
	}
	fmt.Fprintf(&body, "## План работ (шаг %s)\n\n", stepID)
	fmt.Fprintf(&body, "Объём реализованных работ плана: %d из %d задач.\n\n", doneCount, total)
	body.WriteString("| Задача | Название | Роль | Статус |\n")
	body.WriteString("|---|---|---|---|\n")
	for _, t := range tasks {
		status := "запланировано"
		if doneTasks[t.TaskID] {
			status = "выполнено"
		}
		fmt.Fprintf(&body, "| %s | %s | %s | %s |\n", escapeCell(t.TaskID), escapeCell(truncateText(t.Title, 50)), escapeCell(truncateText(t.AssignedRole, 30)), status)
	}
	fmt.Fprintf(&body, "\nРеализовано: %d из %d задач (%.0f%%).\n", doneCount, total, float64(doneCount)*100/float64(max(total, 1)))

	block := openMarker + "\n" + body.String() + closeMarker + "\n"

	existing, err := os.ReadFile(readme)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(existing)
	if start := strings.Index(content, openMarker); start >= 0 {
		if end := strings.Index(content[start:], closeMarker); end >= 0 {
			replaced := content[:start] + block + content[start+end+len(closeMarker):]
			return os.WriteFile(readme, []byte(replaced), 0644)
		}
	}
	// Секции ещё нет — добавляем в конец (или создаём README).
	updated := content
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += "\n" + block
	return os.WriteFile(readme, []byte(updated), 0644)
}

// escapeCell экранирует содержимое ячейки markdown-таблицы (вертикальные
// пайпы и переводы строк, ломающие разметку).
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// truncatedStepError обрабатывает исчерпание лимита раундов в шаге-декомпозиции
// лида. Шаг атомарен и при resume выполняется заново целиком, поэтому
// сохранённая история очищается; при отключённом чекпоинте допускается
// продолжить с частичным результатом (вернёт nil).
func (e *Executor) truncatedStepError(ctx context.Context, step *Step, resp *runner.AgentResponse) error {
	if e.store != nil {
		if snap, err := e.store.Load(ctx); err == nil {
			_ = e.store.ClearRoundState(ctx, snap, step.ID)
		}
		return fmt.Errorf("шаг %q: исчерпан лимит раундов (%d) агентского цикла — запустите с --resume, шаг выполнится заново", step.ID, resp.Rounds)
	}
	if strings.TrimSpace(resp.Content) == "" {
		return fmt.Errorf("шаг %q: модель вернула пустой ответ (исчерпан лимит раундов: %d)", step.ID, resp.Rounds)
	}
	logging.Warnf("[%s] шаг %s: цикл исчерпал лимит раундов (%d), но чекпоинт отключён, продолжаю с частичным результатом", agentLabel(step.Agent, step.Role), step.ID, resp.Rounds)
	return nil
}

// leadTaskPrompt формирует задание специалисту по задаче из JSON-декомпозиции
// лида (plan-режим). В отличие от Kanban-задач, в plan-режиме доска не
// подключена, поэтому подсказки про статусы доски отсутствуют.
func leadTaskPrompt(project string, t board.TaskSpec) string {
	return fmt.Sprintf("Ты — специалист (%s), выполняешь задачу из декомпозиции лида проекта %q.\n\nЗадача: %s — %s\n\nПостановка задачи:\n%s\n\nПравила:\n- Работай в своей выходной директории (OutputDir): учи структуру через List, читай контракты через ReadFiles.\n- Выполни задачу, прогони сборку и проверки через Run, доведи до зелёного состояния.\n- Не выходи за пределы своей части монорепозитория (роль задана промптом).",
		t.AssignedRole, project, t.TaskID, t.Title, t.Description)
}

// printDecomposedTasks выводит в консоль список задач из JSON-декомпозиции
// лида (plan-режим), чтобы человек видел итог декомпозиции до реализации.
func printDecomposedTasks(tasks []board.TaskSpec) {
	logging.Infof("[Список задач лида] %d задач:", len(tasks))
	for _, t := range tasks {
		logging.Infof("  - %s [%s] -> %s (порядок %d, параллельно: %v)",
			t.TaskID, truncateText(t.Title, 60), truncateText(t.AssignedRole, 30),
			t.SequenceOrder.Int(), t.CanRunParallel.Bool())
	}
}

// sortTaskSpecs сортирует задачи декомпозиции по sequence_order, затем по
// TaskID (детерминизм). Порядок последовательный: сначала выполняются
// инфраструктурные и базовые задачи, затем зависимые.
func sortTaskSpecs(tasks []board.TaskSpec) {
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i].SequenceOrder.Int(), tasks[j].SequenceOrder.Int()
		if a != b {
			return a < b
		}
		return tasks[i].TaskID < tasks[j].TaskID
	})
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

// leadStepScope возвращает scope специалистов шага-декомпозиции лида.
// Планировщик даёт шагу точечный scope (конкретные новые файлы, например
// frontend/package.json), но лид декомпозирует шаг на задачи, затрагивающие
// произвольные файлы всей области (компоненты, стили, сервисы). Сужать
// специалистов до одного файла из плана нельзя — они не смогут писать свои
// файлы («вне области работы»). Поэтому специалистам выставляется корневая
// директория шага (первый сегмент: frontend/, server/), а не перечень файлов.
func leadStepScope(projectDir string, step *Step) []string {
	var scope []string
	if len(step.Scope) > 0 {
		if top := topLevelScopeDir(step.Scope); top != "" {
			scope = []string{top + "/"}
		} else {
			scope = effectiveCreationScope(projectDir, step.Scope)
		}
	}
	// README проекта всегда доступен специалистам: лид документирует в нём
	// план работ, и каждый следующий разработчик должен прочитать раздел
	// «План работ», чтобы понять, что уже реализовано.
	if readme := readmePlanName(projectDir); readme != "" {
		scope = append(scope, readme)
	}
	return scope
}

// readmePlanName возвращает имя файла README в корне проекта (реально
// существующий, без учёта регистра), либо "README.md", если его ещё нет.
// Используется лидами и оркестратором для ведения раздела «План работ».
func readmePlanName(projectDir string) string {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "README.md"
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(strings.ToLower(e.Name()), "readme") {
			return e.Name()
		}
	}
	return "README.md"
}

// effectiveCreationScope расширяет scope шага для СОЗДАНИЯ файлов. Если запись
// scope указывает на конкретный файл, которого ещё нет на диске, шаг создаёт
// его с нуля, и модель может выбрать фактическое имя (например, .js вместо
// .tsx из плана). Точное имя из плана в этом случае лишь зацикливает шаг на
// ошибках «вне области работы», и файлы не появляются. Поэтому для новых
// файлов scope расширяется до родительской директории; существующие файлы
// (шаги правки) остаются точечными, как задал планировщик.
func effectiveCreationScope(projectDir string, scope []string) []string {
	if len(scope) == 0 {
		return scope
	}
	out := make([]string, 0, len(scope))
	changed := false
	for _, s := range scope {
		normalized := strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
		trimmed := strings.TrimSuffix(normalized, "/")
		// Директории (со слэшем на конце) не трогаем.
		if trimmed != normalized || trimmed == "" {
			out = append(out, s)
			continue
		}
		full := filepath.Join(projectDir, filepath.FromSlash(trimmed))
		if _, err := os.Stat(full); os.IsNotExist(err) {
			if dir := filepath.ToSlash(filepath.Dir(trimmed)); dir != "." && dir != "" {
				out = append(out, dir+"/")
				changed = true
				continue
			}
		}
		out = append(out, s)
	}
	if !changed {
		return scope
	}
	return out
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
		if e.plan.Steps[i].Agent == AgentQAEngineer {
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
				if fs.Agent == AgentQAEngineer {
					// Модель проигнорировала запрет: приёмку запускает цикл ниже.
					logging.Detailf("[приёмка] раунд %d: шаг qa в плане исправлений пропущен", round)
					continue
				}
				// Фикс-шаг не должен быть лид-типа: лид декомпозирует задачи, а в
				// цикле исправлений нужен один точечный правщик кода. Модель часто
				// возвращает backendlead/frontendlead — нормализуем в developer
				// соответствующей специализации по роли/подпроекту.
				switch fs.Agent {
				case AgentFrontendLead:
					logging.Detailf("[приёмка] раунд %d: лид-шаг %s приведён к frontend-developer", round, fs.Agent)
					fs.Agent = AgentFrontendDev
				case AgentBackendLead:
					logging.Detailf("[приёмка] раунд %d: лид-шаг %s приведён к backend-developer", round, fs.Agent)
					fs.Agent = AgentBackendDev
				}
				step := fs
				step.ID = fixStepID(e, round, executed)
				// Обновлённая область видимости: используем файлы из отчёта
				// приёмки, чтобы фикс-агента не заблокировала запись нужных
				// исходников. Если планировщик не указал scope — подставляем
				// файлы из отчёта.
				// Если приёмка вовсе не смогла указать конкретные файлы
				// (например, тип проекта ещё не определён — нет go.mod или
				// package.json, замечания без файлов) — scope принудительно
				// обнуляем: сужать область догадками планировщика нельзя,
				// иначе исправление упрётся в «вне области работы» и не
				// сможет записать нужные файлы.
				if files := rep.IssueFiles(); len(files) == 0 {
					step.Scope = nil
				} else if len(step.Scope) == 0 {
					step.Scope = files
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
- Все шаги — только агенты-разработчики backend или frontend, выбранные по принадлежности файлов к подпроекту (server/→backend, frontend/→frontend).
- НЕ добавляй шаг qa — повторную сборку, тестирование и приёмку запустит исполнитель.
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

// leadWithHint оборачивает агента-лида для повторного запроса декомпозиции:
// одна попытка на тот же промпт, но с подсказкой, что предыдущий ответ не был
// JSON. Новый контекст (чистый), поэтому подсказка кладётся в системные
// сообщения, чтобы модель сразу её видела.
func leadWithHint(lead agents.Agent, prevContent string) agents.Agent {
	hint := "Ваш предыдущий ответ не был распознан как JSON-декомпозиция задач.\n" +
		"Верните ТОЛЬКО допустимый JSON — массив объектов вида:\n" +
		`[{"title":"...","description":"...","role":"backend|frontend","files":["..."]}]` + "\n" +
		"без пояснений, markdown-обёрток и путей. Роль — одна из: backend, frontend, devops." +
		"\n\nВаш предыдущий ответ, чтобы не повторять его ошибки:\n" + prevContent
	return &leadHintAgent{inner: lead, hint: hint}
}

// leadHintAgent — декоратор agents.Agent, добавляющий подсказку к системным
// сообщениям лида (остальное делегирует внутреннему агенту).
type leadHintAgent struct {
	inner agents.Agent
	hint  string
}

func (h *leadHintAgent) GetSystemMessages(text []agents.Message) []agents.Message {
	base := h.inner.GetSystemMessages(text)
	return append(base, agents.Message{Type: agents.MessageTypeSystem, Message: h.hint})
}

func (h *leadHintAgent) GetUserMessages() []agents.Message       { return h.inner.GetUserMessages() }
func (h *leadHintAgent) GetTools() []tools.ToolDefinition        { return h.inner.GetTools() }
func (h *leadHintAgent) GetToolsForOllama() []api.Tool           { return h.inner.GetToolsForOllama() }
func (h *leadHintAgent) CallFunction(name string, args map[string]any) ([]byte, error) {
	return h.inner.CallFunction(name, args)
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
