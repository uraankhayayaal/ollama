package planner

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/agents/codereviewer"
	"ai/agents/refactor"
	"ai/checkpoint"
	"ai/forges"
	"ai/models"
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
	// store — Redis-хранилище чекпоинтов; nil — контрольные точки отключены.
	store *checkpoint.Store
	// resume — возобновлять ли выполнение с чекпоинта.
	resume bool
}

// NewExecutor создаёт исполнителя плана.
func NewExecutor(provider models.LLMProvider, plan *Plan) *Executor {
	return &Executor{
		provider:  provider,
		plan:      plan,
		completed: make(map[string]bool),
		statuses:  make(map[string]string),
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

	return nil
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

	if resp, err := e.provider.Generate(ctx, agent); err != nil {
		return err
	} else if resp != nil && resp.Truncated && strings.TrimSpace(resp.Content) == "" {
		// Модель исчерпала лимит генерации и вернула пустой ответ без
		// вызовов инструментов — код не создан. Считаем шаг упавшим, чтобы
		// чекпоинт пометил его как failed и можно было повторить через resume.
		return fmt.Errorf("агент %T не создал код: модель вернула пустой ответ (исчерпан лимит токенов генерации)", agent)
	}

	// Self-review сгенерированного/рефакторенного кода — тоже только по
	// файлам из области работы шага.
	if sr, ok := agent.(selfReviewer); ok {
		runRepairLoop(ctx, e.provider, sr, step.Scope, step.Prompt)
	}

	return nil
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
