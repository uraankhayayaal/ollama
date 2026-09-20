package server

import (
	"context"
	"fmt"
	"strings"

	"ai/agents/chatassist"
	"ai/board"
	"ai/chat"
	"ai/models"
	"ai/projects"
	"ai/rag"
	"ai/runevents"
)

// --- Ассистент проекта (Ф-1) ---
//
// Любое сообщение пользователя идёт единому ассистенту (chatassist): он сам
// ПО СМЫСЛУ решает — создать эпик/задачу/баг (инструменты Board*), ответить
// по RAG/доске/файлам или задать уточняющий вопрос. Бинарный сплит по ключевым
// словам (isChatTaskRequest, «создай эпик → доска планировщику», «остальное →
// болтовня») убран: триггер «класть задачу на доску» — это вызов инструмента
// моделью, а не регэкспеп.

// runChatAssistant отвечает на сообщение пользователя в отдельной горутине:
// строит промпт с состоянием проекта и доски, вызывает ассистента (у которого
// есть write-инструменты доски, семантический поиск CodeSearch по RAG и блок
// «релевантный код» в промпте) и публикует ответ в чат.
func (sess *Session) runChatAssistant(ctx context.Context, question string, provider models.LLMProvider) {
	sess.wg.Add(1)
	go func() {
		defer sess.wg.Done()
		prompt := sess.chatAssistantPrompt(question)
		// Работаем в каталоге зарегистрированного проекта (Root), а не в новой
		// temp/<имя>: ассистент должен читать те же файлы, что видит дашборд.
		dir := projects.ProjectDir(sess.project)
		if inf, err := sess.srv.reg.Get(sess.project); err == nil {
			dir = inf.Root
		}
		// Векторная память (RAG): опциональна и ленива. Без Qdrant/эмбеддингов
		// ассистент получает промпт без блока «релевантный код», а CodeSearch
		// деградирует в skipped (как у планировщика).
		ragClient := rag.NewClientSafe(rag.Config{})
		asst := chatassist.NewAssistantInDir(dir, sess.project, prompt, sess.board, ragClient)
		// Репортёр в контексте диалога: ответ ассистента и потребление токенов
		// транслируются в живую шину (type=chat_delta/chat, type=tokens), иначе
		// счётчик токенов чата остаётся на нулях.
		rctx := runevents.WithReporter(ctx, sess.router.WithAgent("assistant"))
		rep, err := provider.Generate(rctx, asst)
		if err != nil {
			sess.append(chat.RoleStatus, "Ошибка: "+err.Error(), "", "", nil)
			return
		}
		if rep == nil || strings.TrimSpace(rep.Content) == "" {
			sess.append(chat.RoleAssistant, "Не расслышал — уточните, пожалуйста. Могу рассказать о состоянии проекта и работах на доске, создать эпик/задачу/баг или просто поболтать.", "assistant", "", nil)
			return
		}
		sess.append(chat.RoleAssistant, rep.Content, "assistant", "", nil)
	}()
}

// chatAssistantPrompt собирает контекст для ассистента: сообщение пользователя,
// состояние оркестрации и компактную сводку Kanban-доски (эпики, задачи, баги).
// Доска также доступна ассистенту напрямую через Board* инструменты чтения,
// блок «релевантный код» (RAG) добавляет сам ассистент в системный промпт.
func (sess *Session) chatAssistantPrompt(question string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Вопрос пользователя:\n%s\n", question)

	sess.mu.Lock()
	running, gating := sess.running, sess.gating
	sess.mu.Unlock()
	switch {
	case gating:
		b.WriteString("\nСостояние оркестрации: ожидает решения человека по текущему затвору.\n")
	case running:
		b.WriteString("\nСостояние оркестрации: идёт выполнение задачи.\n")
	default:
		b.WriteString("\nСостояние оркестрации: ничего не выполняется.\n")
	}

	v, err := boardView(context.Background(), sess.board)
	if err != nil {
		fmt.Fprintf(&b, "\nKanban-доска недоступна: %v. Отвечай по файлам проекта.\n", err)
		return b.String()
	}

	if v.Meta != nil && v.Meta.Task != "" {
		fmt.Fprintf(&b, "\nИсходная задача проекта [%s]: %s\n", v.Meta.Status.Label(), truncateText(v.Meta.Task, 300))
	}

	// Эпики.
	if len(v.Epics) == 0 {
		b.WriteString("\nЭпиков нет.\n")
	} else {
		b.WriteString("\nЭпики:\n")
		for _, e := range v.Epics {
			fmt.Fprintf(&b, "- %s [%s], задач: %d\n", truncateText(e.Title, 120), e.Status.Label(), len(e.Tasks))
		}
	}

	// Задачи: сводка по статусам + список в работе.
	taskByStatus := map[board.Status]int{}
	var inProgress []*board.Task
	for _, t := range v.Tasks {
		taskByStatus[t.Status]++
		if t.Status == board.StatusInProgress {
			inProgress = append(inProgress, t)
		}
	}
	if len(v.Tasks) == 0 {
		b.WriteString("\nЗадач нет.\n")
	} else {
		b.WriteString("\nЗадачи по статусам:\n")
		for _, st := range []board.Status{board.StatusNew, board.StatusAnalysis, board.StatusReady, board.StatusInProgress, board.StatusDone, board.StatusCancelled} {
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
			fmt.Fprintf(&b, "- %s (исполнитель: %s)\n", truncateText(t.Title, 120), who)
		}
	}

	// Баги: сводка по статусам.
	bugByStatus := map[board.BugStatus]int{}
	for _, bg := range v.Bugs {
		bugByStatus[bg.Status]++
	}
	if len(v.Bugs) == 0 {
		b.WriteString("\nБагов нет.\n")
	} else {
		b.WriteString("\nБаги:\n")
		for _, st := range []board.BugStatus{board.BugStatusNew, board.BugStatusConfirmed, board.BugStatusSlop, board.BugStatusFix, board.BugStatusFixed} {
			if n := bugByStatus[st]; n > 0 {
				fmt.Fprintf(&b, "- %s: %d\n", st.Label(), n)
			}
		}
	}

	return b.String()
}
