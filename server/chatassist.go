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
	"ai/runevents"
)

// --- Q&A-ассистент ---
//
// Сообщения пользователя делятся на два типа:
//   - ЯВНЫЙ запрос на создание эпика/задачи (isChatTaskRequest) → эпик на
//     доске планировщику (тот забирает его кнопкой «Продолжить»);
//   - всё остальное (вопросы, статусы, свободный диалог — «подскажи погоду»)
//     → отвечает Q&A-ассистент (chatassist), не трогая доску и файлы.

// taskRequestPhrases — фразы явного запроса на создание эпика/задачи на доске.
// Эвристика намеренно предсказуемая: без явной просьбы «создать задачу» чат
// не создаёт эпики и просто общается с моделью.
var taskRequestPhrases = []string{
	"создай эпик", "создать эпик", "создайте эпик",
	"создай задачу", "создать задачу", "создайте задачу",
	"создай таску", "создать таску", "создай бэклог", "создать бэклог",
	"добавь эпик", "добавить эпик", "добавь задачу", "добавить задачу",
	"добавь таску", "добавить таску", "добавь на доску", "добавить на доску",
	"новый эпик", "новая задача", "новую задачу", "новую таску",
	"заведи задачу", "завести задачу", "заведи эпик", "завести эпик",
	"оформи задачу", "оформить задачу", "оформи эпик", "оформить эпик",
	"поставь задачу", "поставить задачу", "поставь эпик", "поставить эпик",
}

// isChatTaskRequest определяет, что сообщение пользователя — ЯВНЫЙ запрос
// создать эпик/задачу на доске планировщику (например, «создай задачу …»,
// «добавь эпик …», «задача: …»). Только в этом случае чат кладёт задачу на
// доску. Всё остальное — свободный диалог с моделью: вопросы, болтовня,
// «подскажи погоду» — эпики не создаются.
func isChatTaskRequest(msg string) bool {
	s := strings.ToLower(strings.TrimSpace(msg))
	if s == "" {
		return false
	}
	// Явные маркеры вида «задача: сделай витрину» / «эпик: …».
	for _, p := range []string{"задача:", "эпик:", "таска:", "таск:"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	for _, p := range taskRequestPhrases {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// runChatAssist отвечает на вопрос пользователя в отдельной горутине: строит
// промпт с состоянием проекта и доски, вызывает Q&A-ассистента и публикует
// ответ в чат. Оркестрацию не запускает и доску не меняет.
func (sess *Session) runChatAssist(ctx context.Context, question string, provider models.LLMProvider) {
	sess.wg.Add(1)
	go func() {
		defer sess.wg.Done()
		prompt := sess.chatAssistPrompt(question)
		// Работаем в каталоге зарегистрированного проекта (Root), а не в новой
		// temp/<имя>: ассистент должен читать те же файлы, что видит дашборд.
		dir := projects.ProjectDir(sess.project)
		if inf, err := sess.srv.reg.Get(sess.project); err == nil {
			dir = inf.Root
		}
		asst := chatassist.NewAssistantInDir(dir, prompt, sess.board)
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
			sess.append(chat.RoleAssistant, "Не расслышал — уточните, пожалуйста. Могу рассказать о состоянии проекта и работах на доске или просто поболтать.", "assistant", "", nil)
			return
		}
		sess.append(chat.RoleAssistant, rep.Content, "assistant", "", nil)
	}()
}

// chatAssistPrompt собирает контекст для Q&A-ассистента: вопрос пользователя,
// состояние оркестрации и компактную сводку Kanban-доски (эпики, задачи, баги).
// Доска также доступна ассистенту напрямую через Board* инструменты чтения.
func (sess *Session) chatAssistPrompt(question string) string {
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
