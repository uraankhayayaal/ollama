package server

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"ai/agents/chatassist"
	"ai/board"
	"ai/chat"
	"ai/models"
	"ai/projects"
)

// --- Q&A-ассистент ---
//
// Сообщения пользователя в чате делятся на два типа:
//   - вопросы/запросы статуса → отвечает Q&A-ассистент (chatassist), не
//     трогая доску и файлы;
//   - задачи на разработку → запускается оркестрация (планировщик/ло всё
//     агентное). Классификация сделана эвристикой isChatQuestion.

// isChatQuestion определяет, является ли сообщение вопросом или запросом
// статуса (отвечает Q&A-ассистент) вместо задачи на разработку (запускает
// оркестрацию). Эвристика простая и предсказуемая: знак вопроса, начало с
// вопросительного слова или запрос статуса/прогресса/состояния.
func isChatQuestion(msg string) bool {
	s := strings.TrimSpace(msg)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)

	// 1. Явный знак вопроса.
	if strings.ContainsRune(low, '?') {
		return true
	}

	// 2. Многословные фразы запроса состояния/прогресса.
	for _, p := range []string{
		"что делает", "что происходит", "что сейчас", "что было", "чем занят",
		"как дела", "как там", "на каком этапе", "на какой стадии", "где остановился",
		"какой прогресс", "каков прогресс", "сколько осталось", "сколько сделано",
		"какие задачи в работе", "что выполнено", "что готово", "расскажи про",
		"расскажи о", "объясни", "покажи", "опиши", "поясни", "подведи итог",
	} {
		if strings.Contains(low, p) {
			return true
		}
	}

	// 3. Отдельное слово «статус» (в любой позиции). Множественное «статусы»
	// чаще означает задачу («добавь статусы в отчёт») — его не берём.
	for _, w := range words(low) {
		if w == "статус" {
			return true
		}
	}

	// 4. Начало сообщения с вопросительного слова.
	ws := words(low)
	if len(ws) == 0 {
		return false
	}
	switch ws[0] {
	case "что", "как", "почему", "зачем", "где", "куда", "откуда", "когда",
		"кто", "какой", "какая", "какое", "какие", "каких", "чей", "чья", "чьё", "чьи",
		"можно", "сколько", "есть", "верно", "правда":
		return true
	}
	return false
}

// words разбивает строку на слова (последовательности букв/цифр).
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
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
		rep, err := provider.Generate(ctx, asst)
		if err != nil {
			sess.append(chat.RoleStatus, "Ошибка: "+err.Error(), "", "", nil)
			return
		}
		if rep == nil || strings.TrimSpace(rep.Content) == "" {
			sess.append(chat.RoleAssistant, "Не расслышал вопрос — уточните, пожалуйста. Я могу рассказать о состоянии проекта, работах на доске и структуре кода.", "assistant", "", nil)
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
