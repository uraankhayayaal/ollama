package server

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"ai/agents/chatassist"
	"ai/board"
	"ai/chat"
	"ai/models"
	"ai/projects"
	"ai/rag"
	"ai/runctx"
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
		// История диалога: последние реплики пользователя/ассистента в системный
		// промпт, чтобы модель помнила, «о чём писали минуту назад», а не только
		// текущий вопрос. Текущая реплика (последняя запись стрима) исключается.
		asst.History = sess.chatDialogueHistory(ctx)
		// Ф-3: мосты-инструменты к серверным git/канбан-действиям живут вне
		// общего реестра tools (цикл импортов) — инъектируем их в набор
		// ассистента на стороне сервера.
		asst.AddTools(sess.serverActionTools()...)
		// Репортёр в контексте диалога: ответ ассистента и потребление токенов
		// транслируются в живую шину (type=chat_delta/chat, type=tokens), иначе
		// счётчик токенов чата остаётся на нулях.
		var finalReported atomic.Bool
		chatReporter := runevents.NewRouter(func(ev runevents.Event) {
			if ev.Type == runevents.TypeMessage {
				// Пустой финал заменяется человекочитаемым уточнением ниже.
				if strings.TrimSpace(ev.Content) == "" {
					return
				}
				finalReported.Store(true)
			}
			sess.chatEvent(ev)
		}).WithAgent("assistant")
		rctx := runevents.WithReporter(ctx, chatReporter)
		// Расширенное сжатие истории (Ф-6..Ф-11): RAG-вытеснение/ранжирование
		// поверх ragClient и LSP-оглавления проекта. Флаги CODEGEN_HISTORY_*
		// из окружения; выключено по умолчанию.
		rctx = runctx.WithCompression(rctx, sess.project, dir, ragClient)
		rep, err := provider.Generate(rctx, asst)
		if err != nil {
			sess.append(chat.RoleStatus, "Ошибка: "+err.Error(), "", "", nil)
			return
		}
		if rep == nil || strings.TrimSpace(rep.Content) == "" {
			sess.append(chat.RoleAssistant, "Не расслышал — уточните, пожалуйста. Могу рассказать о состоянии проекта и работах на доске, создать эпик/задачу/баг или просто поболтать.", "assistant", "", nil)
			return
		}
		// runner.Generate публикует финальный текст через Reporter.OnMessage,
		// тогда как некоторые реализации LLMProvider возвращают только ответ.
		// Записываем вручную лишь во втором случае.
		if !finalReported.Load() {
			sess.append(chat.RoleAssistant, rep.Content, "assistant", "", nil)
		}
	}()
}

// chatDialogueHistory строит компактную историю диалога из стрима чата:
// последние chatDialogueMaxTurns реплик пользователя/ассистента (и вопросы
// AskUser) в хронологическом порядке. Текущая реплика пользователя — последняя
// запись стрима (добавлена handlePostChat перед вызовом) — пропускается: она
// уже отдельно в «Вопрос пользователя». Возвращает "" — истории нет (пустой
// диалог/ошибка чтения).
func (sess *Session) chatDialogueHistory(ctx context.Context) string {
	const (
		chatDialogueMaxTurns = 12  // сколько последних реплик передаём модели
		chatDialogueMsgLen   = 400 // лимит символов на одну реплику
		chatDialogueHistoryN = 40  // столько читаем из Redis (запас на фильтрацию)
	)
	hist, err := sess.chat.History(ctx, chatDialogueHistoryN)
	if err != nil || len(hist) == 0 {
		return ""
	}

	// Порядок в hist хронологический (старые → новые), последняя запись —
	// только что добавленное сообщение пользователя. Пропускаем её.
	start := 0
	if hist[len(hist)-1].Role == chat.RoleUser {
		start = 1
	}

	var lines []string
	for i := len(hist) - 1 - start; i >= 0; i-- {
		if len(lines) >= chatDialogueMaxTurns {
			break
		}
		m := hist[i]
		switch m.Role {
		case chat.RoleUser, chat.RoleAssistant:
			if txt := strings.TrimSpace(m.Content); txt != "" {
				speaker := "пользователь"
				if m.Role == chat.RoleAssistant {
					speaker = "ассистент"
				}
				lines = append(lines, "["+speaker+"] "+oneLine(txt, chatDialogueMsgLen))
			}
		case chat.RoleAsk:
			if txt := askSummary(m.Ask); txt != "" {
				lines = append(lines, "[ассистент] "+oneLine(txt, chatDialogueMsgLen))
			}
		case chat.RoleTool:
			if txt := strings.TrimSpace(m.Content); txt != "" {
				spyateli := "инструмент"
				if m.Tool != "" {
					spyateli = m.Tool + " →"
				}
				lines = append(lines, "["+spyateli+"] "+oneLine(txt, chatDialogueMsgLen))
			}
		}
	}

	// Собирали свежие → старые; разворачиваем в хронологический порядок.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// oneLine сводит многострочный текст в одну строку (лимит символов).
func oneLine(s string, n int) string {
	return strings.ReplaceAll(truncateText(s, n), "\n", " ")
}

// askSummary собирает текст вопросов структурированного AskUser в одну строку.
func askSummary(a *chat.Ask) string {
	if a == nil {
		return ""
	}
	var parts []string
	for _, q := range a.Questions {
		if t := strings.TrimSpace(q.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "; ")
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

	// Ревизия доски (Ф-2): счётчик изменений с момента старта сессии. Если он
	// вырос с прошлого ответа — доска менялась (пользователь правил, чат создал
	// эпик/задачу/баг, оркестрация продвинула статусы). Модель может учесть
	// это в ответе и не опрашивать доску лишний раз.
	fmt.Fprintf(&b, "\nРевизия доски: %d (счётчик изменений доски с момента запуска).\n", sess.boardRev.Load())

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
		for _, st := range []board.Status{board.StatusNew, board.StatusAnalysis, board.StatusReady, board.StatusInProgress, board.StatusDone, board.StatusCancelled, board.StatusPaused} {
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
