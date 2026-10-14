package chatassist

import (
	"fmt"
	"strings"

	appboard "ai/application/board"
	iboard "ai/infrastructure/board"
)

// BoardView — данные доски для детерминированной сводки промпта ассистента.
type BoardView struct {
	Meta  *iboard.Meta
	Epics []*iboard.Epic
	Tasks []*iboard.Task
	Bugs  []*iboard.BugReport
}

// OrchestrationStatus возвращает строку состояния оркестрации для промпта:
// затвор важнее запуска — при активном подтверждении ассистент не должен
// считать, что задача просто выполняется.
func OrchestrationStatus(running, gating bool) string {
	switch {
	case gating:
		return "\nСостояние оркестрации: ожидает решения человека по текущему затвору.\n"
	case running:
		return "\nСостояние оркестрации: идёт выполнение задачи.\n"
	default:
		return "\nСостояние оркестрации: ничего не выполняется.\n"
	}
}

// BoardRevisionLine сообщает модели ревизию доски (счётчик изменений с момента
// запуска сессии): если он вырос с прошлого ответа, доска менялась, и ассистент
// может учесть это и не опрашивать доску лишний раз.
func BoardRevisionLine(rev uint64) string {
	return fmt.Sprintf("\nРевизия доски: %d (счётчик изменений доски с момента запуска).\n", rev)
}

// BoardSummary собирает компактную сводку Kanban-доски для промпта ассистента:
// исходная задача, эпики, задачи по статусам + список в работе, конфликты
// мёрджа и баги по статусам. Текст детерминирован и не является новым источником
// правды: доска доступна ассистенту и напрямую через Board*-инструменты чтения.
func BoardSummary(v BoardView) string {
	var b strings.Builder

	if v.Meta != nil && v.Meta.Task != "" {
		fmt.Fprintf(&b, "\nИсходная задача проекта [%s]: %s\n", v.Meta.Status.Label(), appboard.TruncateText(v.Meta.Task, 300))
	}

	// Эпики.
	if len(v.Epics) == 0 {
		b.WriteString("\nЭпиков нет.\n")
	} else {
		b.WriteString("\nЭпики:\n")
		for _, e := range v.Epics {
			fmt.Fprintf(&b, "- %s [%s], задач: %d\n", appboard.TruncateText(e.Title, 120), e.Status.Label(), len(e.Tasks))
		}
	}

	// Задачи: сводка по статусам + список в работе.
	taskByStatus := map[iboard.Status]int{}
	var inProgress []*iboard.Task
	for _, t := range v.Tasks {
		taskByStatus[t.Status]++
		if t.Status == iboard.StatusInProgress {
			inProgress = append(inProgress, t)
		}
	}
	if len(v.Tasks) == 0 {
		b.WriteString("\nЗадач нет.\n")
	} else {
		b.WriteString("\nЗадачи по статусам:\n")
		for _, st := range []iboard.Status{iboard.StatusNew, iboard.StatusAnalysis, iboard.StatusReady, iboard.StatusHumanHelp, iboard.StatusInProgress, iboard.StatusTesting, iboard.StatusDone, iboard.StatusCancelled} {
			if n := taskByStatus[st]; n > 0 {
				fmt.Fprintf(&b, "- %s: %d\n", st.Label(), n)
			}
		}
	}
	if len(inProgress) == 0 {
		b.WriteString("Задач в работе нет.\n")
	} else {
		b.WriteString("В работе сейчас:\n")
		for _, t := range inProgress {
			who := t.Assignee
			if who == "" {
				who = t.AssignedRole
			}
			if who == "" {
				who = "без исполнителя"
			}
			fmt.Fprintf(&b, "- %s (исполнитель: %s)\n", appboard.TruncateText(t.Title, 120), who)
		}
	}

	// Конфликты мёрджа (git-workflow Ф-4/Ф-5): ветки задач/эпиков, которые не
	// влились в цель из-за конфликта. Модель видит их явно, чтобы не повторять
	// TaskMerge по кругу, а идти на резолв.
	var conflictEpics, conflictTasks []string
	for _, e := range v.Epics {
		if len(e.MergeConflictFiles) > 0 {
			conflictEpics = append(conflictEpics, fmt.Sprintf("%s [%s]", e.TaskID, strings.Join(e.MergeConflictFiles, ", ")))
		}
	}
	for _, t := range v.Tasks {
		if len(t.MergeConflictFiles) > 0 {
			conflictTasks = append(conflictTasks, fmt.Sprintf("%s [%s]", t.TaskID, strings.Join(t.MergeConflictFiles, ", ")))
		}
	}
	if len(conflictEpics) > 0 || len(conflictTasks) > 0 {
		b.WriteString("\nКонфликты мёрджа (ветка не влилась, нужен резолв):\n")
		if len(conflictEpics) > 0 {
			fmt.Fprintf(&b, "- эпики (main ↔ релизная ветка): %s\n", strings.Join(conflictEpics, "; "))
		}
		if len(conflictTasks) > 0 {
			fmt.Fprintf(&b, "- задачи (ветка ↔ релиз эпика): %s\n", strings.Join(conflictTasks, "; "))
		}
		b.WriteString("НЕ повторяй TaskMerge/EpicRelease при том же конфликте по кругу. Для ЭПИКА (main ↔ релизная ветка) резолв доступен тебе через мосты: ResolveGitConflicts (action=status — конфликтующие файлы и их содержимое, action=start — открыть процесс резолва, action=apply — записать правки) и EpicResolve (приёмка + влитие в main + push, ДЕСТРУКТИВЕН — только после «да»). Для КОНФЛИКТА ЗАДАЧИ (ветка задачи ↔ релизная ветка эпика) автоматического резолва нет — сообщи пользователю файлы и предложи путь (пересоздать ветку задачи, ручной rebase) или спроси через AskUser.\n")
	}

	// Баги: сводка по статусам.
	bugByStatus := map[iboard.BugStatus]int{}
	for _, bg := range v.Bugs {
		bugByStatus[bg.Status]++
	}
	if len(v.Bugs) == 0 {
		b.WriteString("\nБагов нет.\n")
	} else {
		b.WriteString("\nБаги:\n")
		for _, st := range []iboard.BugStatus{iboard.BugStatusNew, iboard.BugStatusConfirmed, iboard.BugStatusSlop, iboard.BugStatusFix, iboard.BugStatusFixed} {
			if n := bugByStatus[st]; n > 0 {
				fmt.Fprintf(&b, "- %s: %d\n", st.Label(), n)
			}
		}
	}

	return b.String()
}
