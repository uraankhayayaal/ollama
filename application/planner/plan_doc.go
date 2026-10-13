package planner

import (
	"ai/board"
	"ai/projects"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const planDocName = "PLAN.md"

// planDocPath возвращает путь к PLAN.md в корне проекта.
func planDocPath(projectName string) string {
	return filepath.Join(projects.ProjectDir(projectName), planDocName)
}

// writePlanDoc перезаписывает PLAN.md полным актуальным планом работ:
// все шаги планировщика, а под каждым лид-шагом — декомпозиция лида с
// подробным контрактом каждой подзадачи. Документ детерминирован и
// регенерируется целиком после каждого шага.
func writePlanDoc(projectName string, plan *Plan) error {
	dir := projects.ProjectDir(projectName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("создание директории проекта для PLAN.md: %w", err)
	}
	return os.WriteFile(planDocPath(projectName), []byte(renderPlanDoc(projectName, plan)), 0o644)
}

// renderPlanDoc собирает markdown-документ плана работ.
func renderPlanDoc(projectName string, plan *Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# План работ — %s\n\n", projectName)
	if strings.TrimSpace(plan.Summary) != "" {
		b.WriteString(strings.TrimSpace(plan.Summary))
		b.WriteString("\n")
	}
	for i := range plan.Steps {
		b.WriteString(renderStepDoc(plan.Steps[i], i+1))
	}
	return b.String()
}

// renderStepDoc рендерит секцию одного шага с вложенной декомпозицией лида.
func renderStepDoc(st Step, num int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n---\n\n## Шаг %d [%s] — %s\n\n", num, agentDisplayName(st.Agent), strings.TrimSpace(st.Description))
	if st.ID != "" {
		fmt.Fprintf(&b, "- **ID:** `%s`\n", st.ID)
	}
	if len(st.Scope) > 0 {
		fmt.Fprintf(&b, "- **Scope:** `%s`\n", strings.Join(st.Scope, "`, `"))
	}
	if strings.TrimSpace(st.Prompt) != "" {
		b.WriteString("\nПостановка:\n\n")
		for _, line := range strings.Split(strings.TrimSpace(st.Prompt), "\n") {
			fmt.Fprintf(&b, "> %s\n", line)
		}
	}
	b.WriteString("\n### Задачи (декомпозиция лида)\n")
	if len(st.Tasks) == 0 {
		b.WriteString("\nДекомпозиция ещё не выполнена: подзадачи появятся после шага лида.\n")
		return b.String()
	}
	tasks := append([]board.TaskSpec{}, st.Tasks...)
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i].SequenceOrder.Int(), tasks[j].SequenceOrder.Int()
		if a != b {
			return a < b
		}
		return tasks[i].TaskID < tasks[j].TaskID
	})
	for i, t := range tasks {
		fmt.Fprintf(&b, "\n#### %d.%d %s — %s\n\n", num, i+1, t.TaskID, strings.TrimSpace(t.Title))
		fmt.Fprintf(&b, "- **Роль:** %s\n", emptyStrDash(t.AssignedRole))
		fmt.Fprintf(&b, "- **Порядок:** %d, параллельно: %v, зависимости: %s\n",
			t.SequenceOrder.Int(), t.CanRunParallel.Bool(), emptyStrDash(strings.Join(t.Dependencies, ", ")))
		if strings.TrimSpace(t.Description) != "" {
			fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(t.Description))
		}
	}
	return b.String()
}

// emptyStrDash возвращает прочерк для пустой строки.
func emptyStrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
