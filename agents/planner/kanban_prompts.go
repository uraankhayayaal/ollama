package planner

import (
	"ai/agents"
	"ai/agents/backendlead"
	"ai/agents/developer"
	"ai/agents/devops"
	"ai/agents/devopslead"
	"ai/agents/frontendlead"
	"ai/agents/qaengineer"
	"ai/board"
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
		ec = &board.StatusCounts{By: map[board.Status]int{}}
	}
	if tc == nil {
		tc = &board.StatusCounts{By: map[board.Status]int{}}
	}
	return fmt.Sprintf("эпиков: %d (выполнено %d, отменено %d), задач: %d (выполнено %d, отменено %d)",
		ec.Total, ec.By[board.StatusDone], ec.By[board.StatusCancelled],
		tc.Total, tc.By[board.StatusDone], tc.By[board.StatusCancelled])
}

// leadFor создаёт агента-лида направления по assigned_role эпика и собирает
// промпт декомпозиции. Лиды пишут ТОЛЬКО скелетон (allowlist в инструменте,
// worktree ветки эпика подключается в phaseLeads через epicOutputDir); для QA
// эпика лидом становится qaengineer.
func (k *KanbanRunner) leadFor(epic *board.Epic) (agents.Agent, error) {
	prompt := k.leadPrompt(epic)
	project := k.store.Project()
	switch {
	case isRole(epic.AssignedRole, "qa", "тест", "testing"):
		return qaengineer.NewQAEngineer(project, prompt), nil
	case isRole(epic.AssignedRole, "devops", "инфра", "infra", "dev", "sre", "ci"):
		return devopslead.NewDevopsLead(project, prompt), nil
	case isRole(epic.AssignedRole, "front", "react", "ui", "client", "фронт"):
		return frontendlead.NewFrontendLead(project, prompt), nil
	default: // backend
		return backendlead.NewBackendLead(project, prompt), nil
	}
}

// bugTriagePrompt формирует задание QA Lead на триаж багрепортов (new):
// подтвердить (BoardSetBugStatus, confirmed) или отсеять «нейрослоп» (slop).
func (k *KanbanRunner) bugTriagePrompt(project string, bugs []*board.BugReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Проведи триаж %d багрепортов QA-специалистов проекта %q с доски.\n\n", len(bugs), project)
	for _, bug := range bugs {
		fmt.Fprintf(&b, "- %s (статус new): %s\n  Репортёр: %s, задача %s, эпик %s\n  %s\n",
			bug.BugID, bug.Title, bug.ReporterRole, bug.TaskID, bug.EpicID, truncateText(bug.Description, 400))
	}
	b.WriteString("\nДля каждого багрепорта вызови BoardSetBugStatus: реальная, воспроизводимая проблема — status=confirmed; надуманное, «нейрослоп», дубль или жалоба без фактов — status=slop.\n")
	b.WriteString("Ответь текстом, что триаж выполнен.")
	return b.String()
}

// bugExpertPrompt формирует задание архитектора (режим экспертизы) по
// подтверждённым багрепортам: вынести вердикт BoardReviewBugReport
// (fix|feature|wont_fix) и при fix создать эпик исправления.
func (k *KanbanRunner) bugExpertPrompt(project string, bugs []*board.BugReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Системный архитектор проекта %q: QA Lead подтвердил %d багрепортов, требуется твоя экспертиза.\n\n", project, len(bugs))
	for i, bug := range bugs {
		fmt.Fprintf(&b, "%d) %s — %s\n   Репортёр %s, задача %s, эпик %s.\n   %s\n",
			i+1, bug.BugID, bug.Title, bug.ReporterRole, bug.TaskID, bug.EpicID, truncateText(bug.Description, 600))
	}
	b.WriteString("\nДля каждого багрепорта вызови BoardReviewBugReport с вердиктом: fix (создай эпик исправления), feature (это фича), wont_fix (не исправляем). Параметры эпика исправления укажи в том же вызове (epic_task_id, epic_title, epic_description, epic_assigned_role, epic_sequence_order).")
	return b.String()
}

// leadPrompt формирует задание лиду: эпик, архитектурная сводка. Лид публикует
// задачи инструментами доски (если они доступны), иначе — JSON-декомпозицией.
// 5.3: НЕ-QA лиды (backend/frontend/devops) дополнительно пишут СКЕЛЕТОН в
// worktree ветки эпика: контракты живут в коде скелетона, задачи ссылаются на
// файлы, а не дублируют контракт текстом. QA-лид скелетона не пишет (у него
// нет Run и работ с кодом — readme-only до Этапа 8), для него сохраняется
// старый формат «полный контракт в description».
func (k *KanbanRunner) leadPrompt(epic *board.Epic) string {
	// Скелетон пишут все лиды, кроме QA: их направления имеют Write/Run и
	// worktree ветки эпика (см. phaseLeads → SetOutputDir).
	skeleton := !isRole(epic.AssignedRole, "qa", "тест", "testing")
	var b strings.Builder
	b.WriteString("Декомпозируй эпик из бэклога Системного архитектора на задачи для рядовых специалистов.\n\n")
	fmt.Fprintf(&b, "Проект: %s\n", k.store.Project())
	fmt.Fprintf(&b, "Эпик: %s — %s\n", epic.TaskID, epic.Title)
	if epic.Summary != "" {
		fmt.Fprintf(&b, "Архитектурная сводка:\n%s\n\n", epic.Summary)
	}
	fmt.Fprintf(&b, "Описание эпика:\n%s\n\n", epic.Description)
	b.WriteString("Публикация задач:\n")
	if skeleton {
		b.WriteString("- Если у тебя есть инструменты доски (BoardCreateTask и др.) — создавай задачи ими (объём, приёмочные критерии и отсылки к файлам скелетона: путь + символ/строка; assigned_role, sequence_order, dependencies). Обновляй и удаляй задачи через BoardUpdateTask/BoardDeleteTask (кроме взятых в работу).\n")
	} else {
		b.WriteString("- Если у тебя есть инструменты доски (BoardCreateTask и др.) — создавай задачи ими (полный контракт в description, assigned_role, sequence_order, dependencies). Обновляй и удаляй задачи через BoardUpdateTask/BoardDeleteTask (кроме взятых в работу).\n")
	}
	b.WriteString("- Если инструментов доски нет — верни строго JSON-декомпозицию (без markdown-обёрток) по схеме:\n")
	b.WriteString("{\n")
	b.WriteString(`  "lead_summary": "краткое техническое описание модуля",` + "\n")
	b.WriteString(`  "tasks": [` + "\n")
	b.WriteString("    {\n")
	b.WriteString(`      "task_id": "уникальный ID (например T-01)",` + "\n")
	b.WriteString(`      "title": "название задачи",` + "\n")
	if skeleton {
		b.WriteString(`      "description": "объём и приёмочные критерии задачи плюс ОБЯЗАТЕЛЬНЫЕ отсылки к файлам скелетона (путь + имя символа), которые ты спроектировал; полный текст контракта не копируется — он зафиксирован в коде скелетона",` + "\n")
	} else {
		b.WriteString(`      "description": "детальное техническое описание задачи с готовым контрактом взаимодействия, который ты спроектировал",` + "\n")
	}
	b.WriteString(`      "assigned_role": "роль специалиста (например Senior Go Developer / React Developer / QA Engineer / DevOps Engineer)",` + "\n")
	b.WriteString("      \"sequence_order\": 1,\n")
	b.WriteString(`      "can_run_parallel": true,` + "\n")
	b.WriteString(`      "dependencies": [` + "\n")
	b.WriteString("      ]\n")
	b.WriteString("    }\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n\n")
	if skeleton {
		b.WriteString("СКЕЛЕТОН В ВЕТКЕ ЭПИКА (обязательно, если OutputDir — git worktree): до публикации задач спроектируй контракты КОДОМ скелетона в ветке эпика — каркасы файлов своего направления (типы/интерфейсы/заготовки конфигов) с комментариями-подсказками, без реализации. Затем прогони проверку через Run (цель из корневого Makefile), закоммить и запуши (git add → git commit → git push). Если .git нет — скелетон не пиши: только декомпозиция.\n\n")
		b.WriteString("ДЕТАЛИЗАЦИЯ ЗАДАЧ (обязательно для каждой задачи): описание обязано содержать отсылки к коду скелетона — файл и символ (путь + имя интерфейса/функции/сервиса, где возможно — строка из ReadMap), объём и приёмочные критерии. ПОЛНЫЙ текст контракта в description НЕ копируй: специалист читает файл скелетона, а не пересказ. Зафиксируй правило: специалист не меняет публичные сигнатуры/схемы из скелетона.\n\n")
	} else {
		b.WriteString("ДЕТАЛИЗАЦИЯ КОНТРАКТОВ (обязательно для каждой задачи): описание задачи обязано содержать ПОЛНЫЙ контракт, по которому специалист пишет код без догадок: точные типы/структуры (поля с типами), публичные сигнатуры функций/методов/интерфейсов (имя, параметры с типами, возвращаемые значения), API-контракты (метод, путь, схема запроса/ответа, коды ошибок), схему БД, манифесты с конкретными значениями. Зафиксируй контракт в description — специалист не меняет публичные сигнатуры и схемы.\n\n")
	}
	b.WriteString("Правила:\n")
	if skeleton {
		b.WriteString("- Ты пишешь ТОЛЬКО скелетон (в ветке эпика), НЕ реализацию: контракты фиксируй кодом скелетона, задачи — отсылками к этим файлам. Run — только проверка (сборка/тесты/автостиль) и git-пуш скелетона; реализацию и удаление файлов выполняют специалисты по твоим задачам.\n")
	} else {
		b.WriteString("- Ты НЕ пишешь код и НЕ запускаешь команды: только проектируешь контракты и раздаёшь задачи.\n")
	}
	b.WriteString("- Порядок разработки: сначала инфраструктура, затем приложение, затем тестирование — проставляй sequence_order и зависимости так, чтобы этот порядок соблюдался (тестовые задачи зависят от прикладных, прикладные — от инфраструктурных).\n")
	if skeleton {
		b.WriteString("- Единый источник контрактов — код скелетона: задачи ссылаются на файлы, контракт не дублируется в описаниях.\n")
	} else {
		b.WriteString("- Контракты взаимодействия дублируй в описание каждой связанной задачи (единый источник истины).\n")
	}
	b.WriteString("- Чётко проставь sequence_order и dependencies: какие задачи параллельны (can_run_parallel: true), какие блокируют друг друга.\n")
	return b.String()
}

// specialistFor создаёт агента-специалиста для задачи: QA/DevOps или
// разработчик (backend/frontend). Разработчик-агент создаёт недостающую
// структуру подпроекта с нуля и дорабатывает существующий код; выбор
// специализации (frontend/backend) идёт по роли задачи (маркеры фронтенда)
// либо по умолчанию — backend.
func (k *KanbanRunner) specialistFor(t *board.Task) (agents.Agent, error) {
	return SpecialistForRole(k.store.Project(), t.AssignedRole, k.taskPrompt(t)), nil
}

// SpecialistForRole создаёт агента-специалиста по роли задачи: QA/DevOps или
// разработчик (backend/frontend). Общий для Kanban- и plan-исполнителей:
// выбор специализации (QA/DevOps/frontend/backend) идёт по роли задачи.
// Экспортирован для server (авторезолвинг конфликтов мёрджа через разработчика).
func SpecialistForRole(project, role, prompt string) agents.Agent {
	switch {
	case isRole(role, "qa", "тест", "testing"):
		return qaengineer.NewQAEngineer(project, prompt)
	case isRole(role, "devops", "инфра", "infra", "sre", "docker", "k8s", "ci"):
		return devops.NewDevops(project, prompt)
	}

	// Разработка кода: задача с фронтенд-маркерами отдаётся Frontend-агенту,
	// всё остальное (включая смешанные/backend/универсальные задачи) — Backend-
	// агенту. Оба агента работают в temp/<project> в корне модуля и сами
	// создают недостающую структуру подпроекта.
	if isRole(role, "front", "react", "ui", "client", "фронт", "js", "ts", "vue") {
		return developer.NewFrontendDeveloper(project, prompt)
	}
	return developer.NewBackendDeveloper(project, prompt)
}

// taskPrompt формирует задание специалисту по задаче с Kanban-доски.
func (k *KanbanRunner) taskPrompt(t *board.Task) string {
	var b strings.Builder
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
	fmt.Fprintf(&b, "- Если у тебя есть инструменты доски: идентификатор задачи %s. Ты можешь читать её контракт (BoardGetTask) и комментарии к ней; ОБЯЗАТЕЛЬНО учти описание и комментарии задачи при выполнении. Когда работа полностью выполнена (сборка и проверки зелёные) — ОБЯЗАТЕЛЬНО переведи задачу в статус testing вызовом BoardSetTaskStatus («На тестирование»).\n", t.TaskID)
	b.WriteString("- Если ты QA-инженер и нашёл дефект по контракту — оформи багрепорт инструментом BoardCreateBugReport (статус new), его разберут QA Lead и архитектор.")
	return b.String()
}

// assignee возвращает ключ специалиста для задачи: присваиваем роль из
// assigned_role (одна задача — один специалист).
func assignee(ts board.TaskSpec) string {
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
func sortTasks(tasks []*board.Task) {
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
func epicList(epics []*board.Epic) string {
	ids := make([]string, 0, len(epics))
	for _, e := range epics {
		ids = append(ids, e.TaskID+" ("+truncateText(e.Title, 40)+")")
	}
	return strings.Join(ids, ", ")
}

// leadName описывает лида направления для логов.
func leadName(epic *board.Epic) string {
	switch {
	case isRole(epic.AssignedRole, "qa", "тест", "testing"):
		return "QA Lead"
	case isRole(epic.AssignedRole, "devops", "инфра", "infra", "dev", "sre", "ci"):
		return "DevOps Lead"
	case isRole(epic.AssignedRole, "front", "react", "ui", "client", "фронт"):
		return "Frontend Lead"
	default:
		return "Backend Lead"
	}
}

// truncateText обрезает длинный текст для логов.
