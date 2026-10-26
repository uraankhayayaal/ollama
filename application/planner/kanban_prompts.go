package planner

import (
	"ai/domain"
	"ai/domain/services"
	"context"
	"fmt"
	"strings"
)

// boardSummary собирает краткую сводку доски для логов и ошибок. Устойчиво к
// отмене контекста/ошибкам хранилища: если счётчики не прочитались (например,
// контекст отменён прямо во время ожидания работы), сводка показывает нули.
func (k *KanbanRunner) boardSummary(ctx context.Context) string {
	ec, _ := k.store.EpicCounts(ctx)
	tc, _ := k.store.TaskCounts(ctx)
	if ec == nil {
		ec = &domain.StatusCounts{By: map[domain.Status]int{}}
	}
	if tc == nil {
		tc = &domain.StatusCounts{By: map[domain.Status]int{}}
	}
	return fmt.Sprintf("эпиков: %d (выполнено %d, отменено %d), задач: %d (выполнено %d, отменено %d)",
		ec.Total, ec.By[domain.StatusDone], ec.By[domain.StatusCancelled],
		tc.Total, tc.By[domain.StatusDone], tc.By[domain.StatusCancelled])
}

// specialistFor создаёт агента-специалиста для задачи: QA/DevOps или
// разработчик (backend/frontend). Разработчик-агент создаёт недостающую
// структуру подпроекта с нуля и дорабатывает существующий код; выбор
// специализации (frontend/backend) идёт по роли задачи (маркеры фронтенда)
// либо по умолчанию — backend.
func (k *KanbanRunner) specialistFor(t *domain.Task) (services.Agent, error) {
	return SpecialistForRole(k.store.Project(), t.AssignedRole, k.taskPrompt(t)), nil
}

// SpecialistForRole создаёт агента-специалиста по роли задачи: QA/DevOps или
// разработчик (backend/frontend). Общий для Kanban- и plan-исполнителей:
// выбор специализации (QA/DevOps/frontend/backend) идёт по роли задачи.
// Экспортирован для server (авторезолвинг конфликтов мёрджа через разработчика).
func SpecialistForRole(project, role, prompt string) services.Agent {
	return agentFactory().Specialist(project, role, prompt)
}

// taskPrompt формирует задание специалисту по задаче с Kanban-доски.
func (k *KanbanRunner) taskPrompt(t *domain.Task) string {
	var b strings.Builder
	nextStatus := "done"
	switch taskPhase(t) {
	case "infra":
		nextStatus = "test_cases"
	case "test":
		nextStatus = "development"
	}
	fmt.Fprintf(&b, "Ты — специалист (%s), выполняешь задачу с общей Kanban-доски проекта %q.\n\n", t.AssignedRole, k.store.Project())
	fmt.Fprintf(&b, "Задача на доске: %s — %s\n\n", t.TaskID, t.Title)
	b.WriteString("Постановка задачи:\n")
	b.WriteString(t.Description)
	b.WriteString("\n\n")
	if len(t.Repositories) > 1 {
		b.WriteString("В задаче участвуют связанные git-репозитории: ")
		b.WriteString(strings.Join(t.Repositories, ", "))
		b.WriteString(". Корень OutputDir содержит их по путям из .gitmodules. Используй пути внутри OutputDir; изменения во вложенных репозиториях будут зафиксированы и опубликованы отдельно.\n\n")
	}
	b.WriteString("Правила:\n")
	b.WriteString("- Работай в своей выходной директории (OutputDir): учи структуру через List, читай контракты через ReadFiles.\n")
	b.WriteString("- Описание задачи — это отсылки к файлам («контракт в ./server/..., см. ./docs/...»), а не полный текст кода или структуры: читай перечисленные файлы из ветки эпика и не вставляй их содержимое в ответы/комментарии.\n")
	b.WriteString("- Выполни задачу, прогони сборку и проверки через Run, доведи до зелёного состояния.\n")
	b.WriteString("- Не выходи за пределы своей части монорепозитория (роль задана промптом).\n")
	fmt.Fprintf(&b, "- Если у тебя есть инструменты доски: идентификатор задачи %s. Ты можешь читать её контракт (BoardGetTask) и комментарии к ней; ОБЯЗАТЕЛЬНО учти описание и комментарии задачи при выполнении. Когда работа полностью выполнена (сборка и проверки зелёные) — ОБЯЗАТЕЛЬНО переведи задачу в статус %s вызовом BoardSetTaskStatus.\n", t.TaskID, nextStatus)
	return b.String()
}

// assignee возвращает ключ специалиста для задачи: присваиваем роль из
// assigned_role (одна задача — один специалист).
func assignee(ts domain.TaskSpec) string {
	if strings.TrimSpace(ts.AssignedRole) == "" {
		return "developer"
	}
	return ts.AssignedRole
}

// isRole проверяет, содержит ли роль (в нижнем регистре) один из маркеров.
func isRole(role string, markers ...string) bool {
	role = strings.ToLower(role)
	for _, m := range markers {
		if strings.Contains(role, m) {
			return true
		}
	}
	return false
}

// sortTasks сортирует задачи по sequence_order, затем по TaskID (детерминизм).
func sortTasks(tasks []*domain.Task) {
	for i := 1; i < len(tasks); i++ {
		for j := i; j > 0; j-- {
			a, b := tasks[j-1], tasks[j]
			if a.SequenceOrder.Int() < b.SequenceOrder.Int() ||
				(a.SequenceOrder.Int() == b.SequenceOrder.Int() && a.TaskID <= b.TaskID) {
				continue
			}
			tasks[j-1], tasks[j] = tasks[j], tasks[j-1]
		}
	}
}

// epicList форматирует список эпиков для логов.
func epicList(epics []*domain.Epic) string {
	ids := make([]string, 0, len(epics))
	for _, e := range epics {
		ids = append(ids, e.TaskID+" ("+truncateText(e.Title, 40)+")")
	}
	return strings.Join(ids, ", ")
}

// truncateText обрезает длинный текст для логов.
