package server

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"ai/agents/chatassist"
	appchatassist "ai/application/chatassist"
	"ai/application/usecases"
	"ai/infrastructure/board"
	"ai/infrastructure/chat"
	"ai/infrastructure/rag"
	"ai/infrastructure/runevents"
	"ai/internal/runctx"
	"ai/shared/projects"
	"ai/tools"
)

// --- Ассистент проекта (Ф-1) ---
//
// Любое сообщение пользователя идёт единому ассистенту (chatassist): он сам
// ПО СМЫСЛУ решает — создать эпик/задачу/баг (инструменты Board*), ответить
// по RAG/доске/файлам или задать уточняющий вопрос. Бинарный сплит по ключевым
// словам (isChatTaskRequest, «создай эпик → доска планировщику», «остальное →
// болтовня») убран: триггер «класть задачу на доску» — это вызов инструмента
// моделью, а не регэкспеп.

// chatAsk — что именно спрашиваем у ассистента в этом ходе.
type chatAsk struct {
	// question — реплика пользователя (может быть пустой при разборе логов).
	question string
	// logDigestPrompt — блок разбора логов для промпта (Ф-5, кнопка
	// «Разобрать»). Пусто в обычных ходах чата.
	logDigestPrompt string
}

// runChatAssistant отвечает на сообщение пользователя в отдельной горутине:
// строит промпт с состоянием проекта и доски, вызывает ассистента (у которого
// есть write-инструменты доски, семантический поиск CodeSearch по RAG и блок
// «релевантный код» в промпте) и публикует ответ в чат.
func (sess *Session) runChatAssistant(ctx context.Context, question string, provider usecases.FullCycle) {
	sess.runChatAsk(ctx, chatAsk{question: question}, provider)
}

// runChatLogAnalysis — ход ассистента, порождённый кнопкой «Разобрать» в
// панели логов. Отличие от обычного: в промпт уходит подготовленный сервером
// дайджест лога, а «реплики пользователя» нет — пользователь не что-то
// писал, он нажал кнопку. Выдумывать от его имени текст в истории нельзя.
func (sess *Session) runChatLogAnalysis(ctx context.Context, a logAnalysis, provider usecases.FullCycle) {
	question := fmt.Sprintf("Разбери лог %s и объясни, что в нём сломалось.", a.FilesLabel())
	sess.runChatAsk(ctx, chatAsk{question: question, logDigestPrompt: LogDigestPrompt(a)}, provider)
}

// runChatAsk — общий запуск ассистента чата.
func (sess *Session) runChatAsk(ctx context.Context, ask chatAsk, provider usecases.FullCycle) {
	sess.wg.Add(1)
	go func() {
		defer sess.wg.Done()
		prompt := sess.chatPromptFor(ask)
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
		asst := chatassist.NewAssistantInDir(dir, sess.project, prompt, board.NewDomainStore(sess.board), ragClient)
		// История диалога: последние реплики пользователя/ассистента в системный
		// промпт, чтобы модель помнила, «о чём писали минуту назад», а не только
		// текущий вопрос. Текущая реплика (последняя запись стрима) исключается.
		asst.History = sess.chatDialogueHistory(ctx)
		// Сессионные инъекции: применяются к следующему вызову модели (hot apply).
		asst.Injections = sess.SessionInjections()
		// Ф-3: мосты-инструменты к серверным git/канбан-действиям живут вне
		// общего реестра tools (цикл импортов) — инъектируем их в набор
		// ассистента на стороне сервера.
		asst.AddTools(sess.serverActionTools()...)

		// Фантомное действие: модель может ОТЧИТАТЬСЯ о создании записи, не
		// вызвав инструмент доски (в логе — «Завёл эпик CHAT-01», tool_calls=0).
		// Тогда в историю чата ушло бы ложное сообщение, а эпика на доске не
		// появилось. Ход повторяем один раз с корректирующей инструкцией, а если
		// и он «соврал» — честно сообщаем статусом, что запись НЕ создана.
		for attempt := 0; attempt < chatWriteAttempts; attempt++ {
			if attempt > 0 {
				asst.Correction = phantomActionCorrection
			}
			var (
				boardWrote    atomic.Bool
				phantom       atomic.Bool
				finalReported atomic.Bool
			)
			// Репортёр в контексте диалога: ответ ассистента и потребление
			// токенов транслируются в живую шину (type=chat_delta/chat,
			// type=tokens), иначе счётчик токенов чата остаётся на нулях.
			chatReporter := runevents.NewRouter(func(ev runevents.Event) {
				// Наблюдаемость (kafkarelay): у ассистента свой параллельный
				// стабильный run_id, отдельный от рана оркестрации эпика.
				if sess.assistantSink != nil {
					sess.assistantSink(ev)
				}
				switch ev.Type {
				case runevents.TypeToolResult:
					// Успешный вызов write-инструмента доски — доказательство
					// реального действия (в т.ч. когда текст-заявление пришёл
					// раньше вызова в том же раунде: заявление было
					// преждевременным, но действие всё-таки свершилось).
					if ev.OK && boardWriteToolNames[ev.Tool] {
						boardWrote.Store(true)
						phantom.Store(false)
					}
				case runevents.TypeMessage:
					// Пустой финал заменяется человекочитаемым уточнением ниже.
					if strings.TrimSpace(ev.Content) == "" {
						return
					}
					if !boardWrote.Load() && looksLikeBoardWriteClaim(ev.Content) {
						// Ложное заявление: доска не менялась, инструмент не
						// вызывался. Не публикуем и не пишем в историю.
						phantom.Store(true)
						return
					}
					finalReported.Store(true)
				}
				// Общий маршрут сессии (не голый chatEvent): события
				// board-инструментов ассистента публикуют снимок доски сразу — в
				// idle-сессии флашера нет, иначе новый эпик/баг появился бы в UI
				// только после обновления страницы.
				sess.routeRunEvent(ev)
			}).WithAgent("assistant")
			rctx := runevents.WithReporter(ctx, chatReporter)
			// Расширенное сжатие истории (Ф-6..Ф-11): RAG-вытеснение/ранжирование
			// поверх ragClient и LSP-оглавления проекта. Флаги CODEGEN_HISTORY_*
			// из окружения; выключено по умолчанию.
			rctx = runctx.WithCompression(rctx, sess.project, dir, ragClient)
			// Рамка прогона для промпт-инъекций: проект и роль заполняют
			// переменные when ({{project.*}}, `when: "role == \"assistant\""`) и
			// шаблоны. Без неё эти условия никогда не срабатывали бы.
			rctx = board.WithInjectionScope(rctx, board.InjectionScope{
				Project: sess.project,
				Role:    "assistant",
			})
			// Сессионные инъекции — не только через asst.Injections (снапшот на
			// старте хода): живой источник перечитывает их перед КАЖДЫМ запросом
			// к модели, так что добавленная посреди стрима инъекция попадёт в
			// следующий раунд того же хода, а не только в следующий ход.
			rctx = board.WithInjectionSource(rctx, func(context.Context) ([]board.Injection, error) {
				return sess.SessionInjections(), nil
			})
			rep, err := provider.Generate(rctx, asst)
			if err != nil {
				sess.append(chat.RoleStatus, "Ошибка: "+err.Error(), "", "", nil)
				return
			}
			if phantom.Load() {
				// Модель весь ход отчитывалась о записи, но доски не касалась —
				// повторяем с явным требованием вызвать инструмент.
				continue
			}
			if rep == nil || strings.TrimSpace(rep.Content) == "" {
				if boardWrote.Load() && !finalReported.Load() {
					// Действие выполнено, но отчёта текстом не было — даём честное
					// подтверждение, а не «не расслышал».
					sess.append(chat.RoleAssistant, "Готово — доска проекта обновлена.", "assistant", "", nil)
					return
				}
				sess.append(chat.RoleAssistant, "Не расслышал — уточните, пожалуйста. Могу рассказать о состоянии проекта и работах на доске, создать эпик/задачу/баг или просто поболтать.", "assistant", "", nil)
				return
			}
			// runner.Generate публикует финальный текст через Reporter.OnMessage,
			// тогда как некоторые реализации LLMProvider возвращают только ответ.
			// Записываем вручную лишь во втором случае.
			if !finalReported.Load() {
				sess.append(chat.RoleAssistant, rep.Content, "assistant", "", nil)
			}
			return
		}
		sess.append(chat.RoleStatus, "Запись на доске НЕ создана: ассистент сообщил о действии, но не вызвал инструмент доски. Повторите запрос или уточните формулировку.", "", "", nil)
	}()
}

// chatWriteAttempts — сколько раз за один ход чата запускается ассистент, если
// он «фантомно» отчитался о действии на доске, не вызвав инструмент. Вторая
// попытка идёт с корректирующей инструкцией (phantomActionCorrection), третьей
// не бывает: если модель неисправима, честный статус важнее расхода токенов.
const chatWriteAttempts = 2

// phantomActionCorrection — корректирующая инструкция для повторного хода:
// прямо называет факт, что прошлый ответ был ложью, и требует выполнить
// действие инструментом в этом же ходе.
const phantomActionCorrection = `ВАЖНО (исправление): в прошлой попытке ты ответил текстом, что создал запись на доске, но НЕ ВЫЗВАЛ НИ ОДНОГО инструмента доски — на доске ничего не появилось, это ложь. Сейчас обязательно вызови нужный инструмент (BoardCreateEpic для эпика, BoardCreateBug для бага, KanbanStart для запуска работы) в этом же ходе и отчитайся об успехе ТОЛЬКО после его status=success. Если обязательных полей не хватает — задай ОДИН вопрос (AskUser). Не повторяй ложный отчёт текстом.`

// boardWriteToolNames — инструменты, успешный вызов которых реально меняет
// доску (создание/правка записей, запуск оркестрации). Чтение (BoardList*,
// BoardGet*) и файловые инструменты сюда не входят: ими действие не
// подтверждается. Имена мостов-действий — константы этого пакета, инструменты
// доски — константы tools.
var boardWriteToolNames = map[string]bool{
	tools.BoardCreateEpic:    true,
	tools.BoardUpdateEpic:    true,
	tools.BoardDeleteEpic:    true,
	tools.BoardSetEpicStatus: true,
	tools.BoardCreateTask:    true,
	tools.BoardUpdateTask:    true,
	tools.BoardDeleteTask:    true,
	tools.BoardSetTaskStatus: true,
	tools.BoardCreateBug:     true,
	tools.BoardSetBugStatus:  true,
	tools.BoardReviewBug:     true,
	actionKanbanStart:        true,
}

// looksLikeBoardWriteClaim — тонкая обёртка над политикой диалога
// (application/chatassist).
func looksLikeBoardWriteClaim(content string) bool {
	return appchatassist.LooksLikeBoardWriteClaim(content)
}

// chatDialogueHistory строит компактную историю диалога из стрима чата:
// последние chatDialogueMaxTurns реплик пользователя/ассистента (и вопросы
// AskUser) в хронологическом порядке. Текущая реплика пользователя — последняя
// запись стрима (добавлена handlePostChat перед вызовом) — пропускается: она
// уже отдельно в «Вопрос пользователя». Возвращает "" — истории нет (пустой
// диалог/ошибка чтения).
// chatDialogueHistory — обёртка над application/chatassist.DialogueHistory
// (Этап 6, срез F3ad): политика форматирования истории диалога переехала в
// application/chatassist. Сигнатура сохранена для существующих вызовов/тестов.
func (sess *Session) chatDialogueHistory(ctx context.Context) string {
	hist, err := sess.chat.History(ctx, appchatassist.DialogueHistoryN)
	if err != nil {
		return ""
	}
	return appchatassist.DialogueHistory(hist)
}

// --- Якорь действия (антизацикливание) ---
//
// Проблема, которую он снимает: ассистент на каждом ходе заново решает, что
// делать, и не помнит, что уже спрашивал. Пользователь отвечает «да», а модель
// снова спрашивает то же самое («Создать такой эпик? → да → Запустить? → да →
// Запустить Kanban? → да → …»), и ни одного инструмента не вызывается. История
// диалога тут не спасает: она обрезана и в общем случае не разбирается.
//
// Поэтому сервер, у которого есть полный стрим чата, вычисляет ситуацию и
// кладёт в промпт ОДНУЗНАЧНУЮ инструкцию: подтверждение получено — выполняй,
// либо вопрос задан второй раз подряд — прекрати переспрашивать. Инструкция
// не запрещает ничего, чего нет в системном промпте, и не является новым
// источником правды: она лишь однозначно маркирует состояние диалога.

// chatActionAnchor читает историю чата и делегирует политику диалога
// (application/chatassist.ActionAnchor): подсказка, однозначно говорящая модели,
// что делать в этом ходе (см. комментарий в пакете).
func chatActionAnchor(store *chat.Store, question string) string {
	hist, err := store.History(context.Background(), 20)
	if err != nil || len(hist) == 0 {
		return ""
	}
	return appchatassist.ActionAnchor(hist, question)
}

// chatAssistantPrompt собирает контекст для ассистента: сообщение пользователя,
// состояние оркестрации и компактную сводку Kanban-доски (эпики, задачи, баги).
// Доска также доступна ассистенту напрямую через Board* инструменты чтения,
// блок «релевантный код» (RAG) добавляет сам ассистент в системный промпт.
// chatAssistantPrompt — промпт ассистента чата для обычной реплики.
func (sess *Session) chatAssistantPrompt(question string) string {
	return sess.chatPromptFor(chatAsk{question: question})
}

// chatPromptFor строит полный промпт хода: вопрос, якорь действия, состояние
// оркестрации, доска, RAG-блок и (для разбора логов) дайджест.
func (sess *Session) chatPromptFor(ask chatAsk) string {
	question := ask.question
	var b strings.Builder
	fmt.Fprintf(&b, "Вопрос пользователя:\n%s\n", question)

	// Якорь действия: ситуативная подсказка, однозначно говорящая модели, что
	// делать в этом ходе. Без неё ассистент на каждом ходе заново решает, что
	// делать, и зацикливается на переспрашивании (см. chatActionAnchor).
	if a := chatActionAnchor(sess.chat, question); a != "" {
		b.WriteString("\n")
		b.WriteString(a)
	}

	sess.mu.Lock()
	running := sess.running
	sess.mu.Unlock()
	gating, _, _ := sess.gateOf().State()
	b.WriteString(appchatassist.OrchestrationStatus(running, gating))

	// Ревизия доски (Ф-2): счётчик изменений с момента старта сессии. Если он
	// вырос с прошлого ответа — доска менялась (пользователь правил, чат создал
	// эпик/задачу/баг, оркестрация продвинула статусы).
	b.WriteString(appchatassist.BoardRevisionLine(sess.boardRev.Load()))

	v, err := boardView(context.Background(), sess.board)
	if err != nil {
		fmt.Fprintf(&b, "\nKanban-доска недоступна: %v. Отвечай по файлам проекта.\n", err)
		return b.String()
	}

	b.WriteString(appchatassist.BoardSummary(appchatassist.BoardView{
		Meta:  v.Meta,
		Epics: v.Epics,
		Tasks: v.Tasks,
		Bugs:  v.Bugs,
	}))

	// Дайджест логов (Ф-5, кнопка «Разобрать»). Идёт последним: он большой и
	// относится к текущему ходу, а не к постоянному состоянию проекта.
	if ask.logDigestPrompt != "" {
		b.WriteString(ask.logDigestPrompt)
	}

	return b.String()
}
