package planner

import (
	"ai/logging"
	"ai/tools"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// runQAEngineer запускает QA-шаг плана и организует цикл «тестирование →
// багрепорты → исправление → повторное тестирование» (бюджет QA_FIX_ROUNDS):
//
//  1. Создание: QA-инженер пишет/обновляет автотесты, запускает их и
//     публикует дефекты в ответе псевдо-вызовами BugReport(...).
//  2. Триаж: исполнитель передаёт багрепорты планировщику, тот оставляет
//     только реальные дефекты (по принадлежности файлов) и составляет шаги
//     исправления backend/frontend — «нейрослоп» в план не попадает.
//  3. Закрытие: повторное тестирование, и когда багрепортов больше нет —
//     все найденные дефекты считаются закрытыми, шаг завершён.
//
// Эпик/план завершается только когда выполнены все задачи и багрепорты.
func (e *Executor) runQAEngineer(ctx context.Context, step *Step, projectName string) error {
	rounds := qaFixRounds()
	qaScope := step.Scope
	openBugs := 0 // число багрепортов, найденных последним прогоном тестирования

	for round := 1; round <= rounds; round++ {
		qaStep := *step
		qaStep.Prompt = qaTestPrompt(step.Prompt)
		qaStep.Scope = qaScope

		resp, err := e.runCodingAgent(ctx, &qaStep, projectName)
		if err != nil {
			return err
		}
		var bugs []tools.BugReport
		if resp != nil {
			bugs = tools.ParseBugReports(resp.Content)
		}

		logging.Infof("[QA-инженер] тестирование (раунд %d/%d, scope: %v): найдено багрепортов: %d",
			round, rounds, qaScope, len(bugs))

		if len(bugs) == 0 {
			if openBugs > 0 {
				logging.Infof("[QA-инженер] багрепортов больше нет — закрыто багрепортов в эпике: %d", openBugs)
			} else {
				logging.Infof("[QA-инженер] тестирование пройдено: дефектов не найдено")
			}
			return nil
		}
		openBugs = len(bugs)

		if round >= rounds {
			return fmt.Errorf("тестирование %q не пройдено после %d раунда(ов) — осталось багрепортов: %d",
				projectName, rounds, len(bugs))
		}

		// Багрепорты найдены: планировщик (триаж) составляет шаги исправления.
		fixes, err := e.planQAFixes(ctx, projectName, bugs)
		if err != nil {
			return fmt.Errorf("раунд тестирования %d: планирование исправлений: %w", round, err)
		}

		executed := 0
		for _, fs := range fixes {
			// Повторное тестирование и приёмку запускают циклы исполнителя,
			// а не планировщик исправлений.
			if fs.Agent == AgentAcceptor || fs.Agent == AgentQAEngineer {
				logging.Detailf("[%s] раунд тестирования %d: шаг %s в плане исправлений пропущен", agentLabel(fs.Agent, fs.Role), round, fs.Agent)
				continue
			}
			fixStep := fs
			fixStep.ID = qaFixStepID(e, round, executed)
			// Область исправления: если планировщик не указал scope —
			// подставляем файлы, на которые указывают багрепорты.
			if len(fixStep.Scope) == 0 {
				fixStep.Scope = bugFiles(bugs)
			}
			e.plan.Steps = append(e.plan.Steps, fixStep)
			logging.Infof("[%s] раунд тестирования %d: шаг исправления %s: %s", agentLabel(fixStep.Agent, fixStep.Role), round, fixStep.ID, fixStep.Description)
			e.markRunning(ctx, fixStep.ID)
			if err := e.executeStep(ctx, &fixStep); err != nil {
				e.markFailed(ctx, fixStep.ID)
				return fmt.Errorf("раунд тестирования %d, шаг исправления %q: %w", round, fixStep.ID, err)
			}
			e.markDone(ctx, fixStep.ID)
			executed++
			qaScope = uniqueSlash(append(qaScope, fixStep.Scope...))
		}
		if executed == 0 {
			logging.Warnf("[QA-инженер] раунд тестирования %d: планировщик не дал шагов исправлений — повторяю тестирование", round)
			continue
		}

		logging.Infof("[QA-инженер] раунд тестирования %d: повторное тестирование после исправлений", round)
	}

	return fmt.Errorf("тестирование %q не пройдено после %d раундов исправлений", projectName, rounds)
}

// planQAFixes отдаёт планировщику багрепорты QA и получает шаги исправления.
// Формат — тот же JSON-план. Планировщик играет роль триажа и архитектора
// (как QA Lead и Системный архитектор в Kanban): исправляет только реальные
// дефекты по контракту, «нейрослоп» в план не включает.
func (e *Executor) planQAFixes(ctx context.Context, projectName string, bugs []tools.BugReport) ([]Step, error) {
	issueFiles := uniqueSlash(bugFiles(bugs))

	var b strings.Builder
	b.WriteString("QA-тестирование проекта выявило дефекты (багрепорты).\n\nБагрепорты:\n")
	for _, bug := range bugs {
		loc := strings.TrimSpace(bug.FilePath)
		if bug.Line > 0 {
			loc += ":" + strconv.Itoa(bug.Line)
		}
		sev := strings.TrimSpace(bug.Severity)
		if sev != "" {
			sev = " [" + strings.ToLower(sev) + "]"
		}
		if loc != "" {
			fmt.Fprintf(&b, "- %s%s %s\n", loc, sev, strings.TrimSpace(bug.Text))
		} else {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(bug.Text))
		}
	}

	b.WriteString(`
Составь план исправлений этих багрепортов.
Требования:
- Проведи триаж: исправляй только реальные дефекты по контракту. «Нейрослоп» (нереальные, дубли, придирки к стилю) в план НЕ включай.
- Все шаги — только агенты-разработчики backend или frontend, выбранные по принадлежности файлов к подпроекту (server/→backend, frontend/→frontend).
- НЕ добавляй шаги acceptor и qa — повторное тестирование и приёмку запустит исполнитель.
- scope каждого шага — ТОЛЬКО файлы, реально требующие правки, обязательно включая:
  `)
	if len(issueFiles) > 0 {
		b.WriteString(bullet(issueFiles))
	} else {
		b.WriteString("  (файлы из багрепортов)")
	}
	b.WriteString("\n- Каждый шаг — одно конкретное исправление.")

	pa := NewPlanner(projectName, b.String())
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

// bugFiles возвращает уникальные нормализованные файлы, на которые указывают
// багрепорты.
func bugFiles(bugs []tools.BugReport) []string {
	seen := map[string]bool{}
	var files []string
	for _, bug := range bugs {
		f := strings.TrimSpace(strings.TrimPrefix(bug.FilePath, "./"))
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	return files
}

// qaFixStepID генерирует уникальный ID шага исправления тестирования, чтобы не
// пересекаться с ID шагов основного плана и других раундов.
func qaFixStepID(e *Executor, round, idx int) string {
	base := fmt.Sprintf("qa-r%d-%d", round, idx)
	if e.findStep(base) == nil {
		return base
	}
	for n := 1; ; n++ {
		id := fmt.Sprintf("qa-r%d-%d-%d", round, idx, n)
		if e.findStep(id) == nil {
			return id
		}
	}
}

// qaFixRounds возвращает бюджет раундов цикла «тестирование → исправление»
// из окружения QA_FIX_ROUNDS (по умолчанию 3).
func qaFixRounds() int {
	const def = 3
	v := strings.TrimSpace(os.Getenv("QA_FIX_ROUNDS"))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// qaTestPrompt дополняет промпт QA-шага требованием публиковать дефекты
// багрепортами в строгом псевдо-call формате (парсится исполнителем).
func qaTestPrompt(prompt string) string {
	const suffix = `

ФОРМАТ БАГРЕПОРТОВ:
Если тесты выявили дефекты по контракту (расхождение с API-контрактами, падающие тесты, ошибки в логах) — заверши ответ перечнем багрепортов:
BugReport(file="путь_к_файлу_или_компоненту", line=Н, severity="blocker|major|minor", text="шаги воспроизведения, ожидаемый результат по контракту и фактический результат")
Если дефектов не найдено — заверши ответ одной строкой: "Багрепортов нет".`
	if strings.TrimSpace(prompt) == "" {
		return strings.TrimSpace(suffix)
	}
	return prompt + suffix
}
