package planner

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/agents/codereviewer"
	"ai/agents/refactor"
	"ai/forges"
	"ai/models"
	"context"
	"fmt"
	"log"
)

// Executor выполняет план поэтапно, создавая нужных агентов.
// Каждый шаг запускается отдельным агентом с чистым контекстом:
// история не накапливается между шагами, что снижает расход токенов.
type Executor struct {
	provider  models.LLMProvider
	plan      *Plan
	completed map[string]bool
}

// NewExecutor создаёт исполнителя плана.
func NewExecutor(provider models.LLMProvider, plan *Plan) *Executor {
	return &Executor{
		provider:  provider,
		plan:      plan,
		completed: make(map[string]bool),
	}
}

// Run выполняет все шаги плана в топологическом порядке с учётом зависимостей.
func (e *Executor) Run(ctx context.Context) error {
	order, err := e.topoSort()
	if err != nil {
		return fmt.Errorf("в плане нарушен порядок шагов: %w", err)
	}

	for _, stepID := range order {
		step := e.findStep(stepID)
		if step == nil {
			return fmt.Errorf("шаг %q не найден в плане", stepID)
		}

		for _, dep := range step.DependsOn {
			if !e.completed[dep] {
				return fmt.Errorf("шаг %q зависит от незавершённого шага %q", stepID, dep)
			}
		}

		log.Printf("[Plan] шаг %s: %s (агент: %s)", step.ID, step.Description, step.Agent)
		if err := e.executeStep(ctx, step); err != nil {
			return fmt.Errorf("шаг %q: %w", step.ID, err)
		}
		e.completed[stepID] = true
		log.Printf("[Plan] шаг %s завершён", step.ID)
	}

	return nil
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

	if _, err := e.provider.Generate(ctx, agent); err != nil {
		return err
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

// topoSort возвращает ids шагов в порядке выполнения (топологическая
// сортировка графа зависимостей). При цикле возвращает ошибку.
func (e *Executor) topoSort() ([]string, error) {
	inDegree := make(map[string]int)
	dependents := make(map[string][]string)

	for _, step := range e.plan.Steps {
		if _, ok := inDegree[step.ID]; !ok {
			inDegree[step.ID] = 0
		}
		for _, dep := range step.DependsOn {
			dependents[dep] = append(dependents[dep], step.ID)
			inDegree[step.ID]++
		}
	}

	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	var order []string
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		for _, dep := range dependents[id] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if len(order) != len(e.plan.Steps) {
		return nil, fmt.Errorf("обнаружен цикл в зависимостях")
	}

	return order, nil
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