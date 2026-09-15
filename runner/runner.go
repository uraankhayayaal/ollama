package runner

import (
	"ai/agents"
	"ai/tools"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// AgentResponse содержит ответ модели и все выполненные вызовы функций.
type AgentResponse struct {
	Content   string
	ToolCalls []tools.ToolCall
	// Truncated указывает, что цикл остановлен по лимиту раундов (maxRounds)
	// или модель обрезалась по лимиту токенов, а не потому, что модель
	// завершила ответ корректно.
	Truncated bool
	// Messages — полная история диалога цикла (system/user + все раунды).
	// Сохраняется вызывающим кодом в чекпоинт для возобновления (resume).
	Messages []Message
	// Rounds — количество уже потраченных раундов цикла. При resume новый
	// запуск продолжит с раунда Rounds+1, получив снова полный бюджет maxRounds.
	Rounds int
}

// Message — нейтральное представление сообщения диалога,
// провайдеры конвертируют его в свой формат.
type Message struct {
	Role       string // "system" | "user" | "assistant" | "tool"
	Content    string
	ToolCalls  []tools.ToolCall // для assistant
	ToolName   string           // для tool
	ToolCallID string           // для tool
}

// ModelReply — ответ модели за один раунд.
type ModelReply struct {
	Content      string
	ToolCalls    []tools.ToolCall
	FinishReason string
}

// ChatProvider — провайдер, умеющий сделать ОДИН запрос к модели.
type ChatProvider interface {
	ChatOnce(ctx context.Context, agent agents.Agent, messages []Message) (*ModelReply, error)
}

// ToolRequiringAgent — необязательный интерфейс агента, который требует,
// чтобы в ПЕРВОМ раунде модель обязательно вызвала определённый инструмент
// (например, WriteFiles у генератора кода). Если модель вместо вызова вернула
// текст — раннер повторит запрос с подсказкой использовать этот инструмент.
type ToolRequiringAgent interface {
	// RequiredToolFirstRound возвращает имя инструмента, обязательного к
	// вызову в первом раунде, и true, если требование активно.
	RequiredToolFirstRound() (string, bool)
}

// RequiredToolGroupsAgent — необязательный интерфейс агента, который требует,
// чтобы за цикл успешно выполнился хотя бы ОДИН инструмент из КАЖДОЙ
// обязательной группы. Используется лидами направлений: они обязаны изучить
// существующий код (List, ReadFiles) и опубликовать задачи для подчинённых
// (BoardCreateTask/BoardUpdateTask/BoardDeleteTask). Если модель ответила
// текстом, не выполнив очередную группу, — раннер подскажет недостающий
// инструмент и повторит запрос.
type RequiredToolGroupsAgent interface {
	// RequiredToolGroups возвращает группы инструментов, обязательных к
	// успешному вызову за цикл. Каждая группа выполняется, если успешно
	// вызван хотя бы один её инструмент. Пустой список — требований нет.
	RequiredToolGroups() [][]string
}

// ResumeState — точка возобновления агентского цикла после лимита раундов:
// полная история диалога и количество уже потраченных раундов. Собирается
// вызывающим кодом из чекпоинта и передаётся в цикл через контекст.
type ResumeState struct {
	Messages []Message
	Rounds   int
}

// resumeStateKey — тип ключа контекста для передачи состояния resume.
type resumeStateKey struct{}

// WithResumeState помещает состояние возобновления агентского цикла в контекст.
// Провайдеры сами не меняются: runner.Generate читает состояние из контекста.
func WithResumeState(ctx context.Context, state *ResumeState) context.Context {
	return context.WithValue(ctx, resumeStateKey{}, state)
}

// ResumeStateFromContext извлекает состояние возобновления из контекста.
func ResumeStateFromContext(ctx context.Context) *ResumeState {
	if st, ok := ctx.Value(resumeStateKey{}).(*ResumeState); ok {
		return st
	}
	return nil
}

// requiredRetries — сколько раз переспрашиваем модель, если она не вызвала
// обязательный инструмент первого раунда и ответила текстом.
const requiredRetries = 2

// maxRoundsLimit ограничивает количество итераций выполнения инструментов,
// чтобы защититься от бесконечного цикла "модель -> инструмент".
// Значение по умолчанию можно переопределить переменной REVIEW_MAX_ROUNDS.
const defaultMaxRounds = 12

func maxRounds() int {
	if v := os.Getenv("REVIEW_MAX_ROUNDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxRounds
}

// nudgeMessage формулирует подсказку модели, если она не выполнила
// обязательное действие (не вызвала обязательный инструмент) и ответила
// текстом.
func nudgeMessage(toolName string) string {
	return fmt.Sprintf("Ты ответил текстом, но не выполнил обязательное действие: инструмент %q не был успешно вызван. Немедленно вызови %q с нужными аргументами. Не отвечай текстом.", toolName, toolName)
}

// retryMessage — подсказка после неудачной попытки обязательного инструмента
// (например, инструмент вернул ошибки аргументов/доступа). Модель должна
// повторить вызов, пока он не завершится успешно.
func retryMessage(toolName string) string {
	return fmt.Sprintf("Инструмент %q вернул ошибки (вероятно, некорректные аргументы или действие вне допустимого). Проверь и повтори вызов %q ДО успешного результата. Не отвечай текстом.", toolName, toolName)
}

// maxRepeatedToolCalls — порог повторения одного и того же вызова инструмента
// (имя + аргументы), после которого раннер подсказывает модели остановить
// цикл. Защищает от бесконечного «исследования»: модель, не получив ничего
// нового (например, лид в пустом проекте), начинает в цикле звать одни и те
// же инструменты, сжигая раунды до лимита (REVIEW_MAX_ROUNDS) и роняя шаг
// «исчерпан лимит раундов» вместо завершения декомпозицией.
const maxRepeatedToolCalls = 4

// maxNeedRefills — сколько раз за цикл раннер может «дозаправить» контекст по
// маркерам NEED_* в финальном ответе модели, прежде чем принять его как есть.
// Ограничение защищает от бесконечной петли «модель просит контекст → раннер
// добавляет → модель просит снова».
const maxNeedRefills = 2

// ContextSupplier — необязательный интерфейс агента, умеющего отдавать
// компактный контекст «по требованию» (карта кода / диапазон строк): карты
// кода экономят контекст, а дозаправка не даёт модели галлюцинировать
// недостающие сигнатуры. Реализуется встроенным в агентов *tools.FileOps
// (FetchContext), поэтому методы промотируются автоматически.
type ContextSupplier interface {
	// FetchContext возвращает компактный контекст по цели вида
	// "path/to/file.go" (карта кода), "path/to/file.go:20-45" или
	// "path/to/file.go 20-45" (интервал строк). false — цель нераспознана.
	FetchContext(target string) (string, bool)
}

// needContextRe — регулярное выражение маркеров дозаправки контекста в
// текстовом ответе модели: NEED_CONTEXT / NEED_SIGNATURE / NEED_FILE с целью
// (путь и, опционально, интервал строк) до конца строки.
var needContextRe = regexp.MustCompile(`(?m)(?:NEED_CONTEXT|NEED_SIGNATURE|NEED_FILE)\s*[:：]?\s*([^\r\n]+)`)

// NeedContextTargets извлекает цели дозаправки контекста (маркеры NEED_*) из
// ответа модели. Возвращает уникальные цели в порядке появления.
func NeedContextTargets(content string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range needContextRe.FindAllStringSubmatch(content, -1) {
		t := strings.TrimSpace(m[1])
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// needContextPull запрашивает у агента (если он реализует ContextSupplier)
// компактный контекст для каждой цели дозаправки из финального ответа.
// Возвращает карту «цель → контекст» и true, если запросили хотя бы один.
func needContextPull(agent agents.Agent, content string) (map[string]string, bool) {
	supplier, ok := agent.(ContextSupplier)
	if !ok {
		return nil, false
	}
	targets := NeedContextTargets(content)
	if len(targets) == 0 {
		return nil, false
	}
	pulled := make(map[string]string, len(targets))
	for _, t := range targets {
		if txt, ok := supplier.FetchContext(t); ok {
			pulled[t] = txt
		}
	}
	return pulled, len(pulled) > 0
}

// contextRefillMessage составляет системное сообщение с дозаправленным
// контекстом (что модель запросила маркерами NEED_*). Детерминированный
// порядок целей — по алфавиту.
func contextRefillMessage(pulled map[string]string) string {
	keys := make([]string, 0, len(pulled))
	for k := range pulled {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("Система дозаправила контекст по твоим маркерам NEED_*. Используй только эти данные и заверши задачу итоговым ответом; НЕ повторяй маркеры NEED_*.\n\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "--- %s ---\n%s\n", k, pulled[k])
	}
	return b.String()
}

// callSignature строит нормализованный ключ вызова инструмента (имя +
// нормализованные аргументы) для детекции повторяющихся циклов. encoding/json
// сортирует ключи map, поэтому одинаковые по смыслу аргументы дают тот же ключ.
func callSignature(name string, args map[string]any) string {
	b, err := json.Marshal(args)
	if err != nil {
		return name
	}
	return name + " " + string(b)
}

// loopMessage — подсказка модели при детекции повторяющихся вызовов.
func loopMessage(toolName string, n int) string {
	return fmt.Sprintf("Ты уже %d раз вызвал инструмент %q с одинаковыми аргументами — это повторяющийся цикл. Прекрати его: опирайся на уже полученные результаты и заверши работу итоговым ответом по требуемой схеме (не вызывая повторно те же инструменты).", n, toolName)
}

// toolResultFailed признаёт выполнение инструмента неудачным, если результат
// помечен ошибкой ("status":"error"). Пустой результат ("status":"empty") —
// неудача ТОЛЬКО для пишущих инструментов (WriteFiles и др.), где это
// означает «ничего не создано». Для исследовательских инструментов (List,
// ReadFiles) пустой каталог/файл — нормальный успешный результат чтения,
// иначе раннер будет вечно подсказывать «вызови List ДО успешного результата»
// при работе на пустом проекте.
//
// Аналогично, пер-файловые ошибки ReadFiles/List ("файл вне области работы
// (scope)", "файл не существует") — не неудача инструмента, а информативный
// результат: scope шага фиксирован, «успешного» повторного чтения не
// существует, а принудительные повторы лишь сжигают раунды и роняют шаг по
// лимиту. Для пишущих инструментов такие ошибки по-прежнему означают
// «ничего не сделано» и требуют повтора.
func toolResultFailed(toolName string, result []byte) bool {
	if bytes.Contains(result, []byte(`"status":"error"`)) {
		switch toolName {
		case "List", "ReadFiles":
			return false
		}
		return true
	}
	if bytes.Contains(result, []byte(`"status":"empty"`)) {
		switch toolName {
		case "List", "ReadFiles":
			return false
		}
		return true
	}
	return false
}

// Generate выполняет агентский цикл: отправляет диалог модели, исполняет
// запрошенные инструменты, возвращает результат модели обратно в историю
// и повторяет, пока модель не завершит ответ (нет tool_calls).
//
// Если в контексте передано состояние возобновления (WithResumeState), цикл
// не начинает заново, а продолжает сохранённый диалог с раунда Rounds+1,
// имея снова полный бюджет maxRounds. Это позволяет «донаточивать» задачу
// повторными запусками: 1..12 → resume 13..24 → resume 25..36 и т.д.
func Generate(ctx context.Context, provider ChatProvider, agent agents.Agent) (*AgentResponse, error) {
	return generate(ctx, provider, agent, ResumeStateFromContext(ctx))
}

func generate(ctx context.Context, provider ChatProvider, agent agents.Agent, resume *ResumeState) (*AgentResponse, error) {
	// Собираем user-сообщения (например, дифф для ревью), чтобы передать
	// их контекст в метод системных сообщений (GetSystemMessages).
	userMessages := agent.GetUserMessages()

	var messages []Message
	startRound := 0
	if resume != nil && len(resume.Messages) > 0 {
		// Возобновление после лимита раундов: история уже содержит системные
		// и user-сообщения, добавлять их повторно нельзя.
		messages = append(messages, resume.Messages...)
		startRound = resume.Rounds
	} else {
		// Системные сообщения размещаем в начале диалога, как это принято,
		// а user-сообщения — следом. Контекст (дифф) передаётся в метод
		// системных сообщений через параметр.
		systemMessages := agent.GetSystemMessages(userMessages)

		for _, m := range systemMessages {
			messages = append(messages, Message{Role: "system", Content: m.Message})
		}
		for _, m := range userMessages {
			messages = append(messages, Message{Role: "user", Content: m.Message})
		}
	}

	// Инструменты, уже вызванные в истории диалога: при resume они должны
	// засчитываться, чтобы проверка «обязательный инструмент первого раунда
	// не вызван» не сработала для уже продвинутого диалога.
	var allToolCalls []tools.ToolCall
	for _, m := range messages {
		if m.Role == "assistant" {
			allToolCalls = append(allToolCalls, m.ToolCalls...)
		}
	}
	content := ""
	mx := maxRounds()

	// Обязательные инструменты собираем из двух источников: обобщённые группы
	// RequiredToolGroups (например, лиды: исследование кода + публикация задач)
	// и классический RequiredToolFirstRound (одна группа из одного инструмента).
	var requiredGroups [][]string
	if rg, ok := agent.(RequiredToolGroupsAgent); ok {
		requiredGroups = rg.RequiredToolGroups()
	}
	if req, ok := agent.(ToolRequiringAgent); ok {
		if n, yes := req.RequiredToolFirstRound(); yes && n != "" {
			requiredGroups = append(requiredGroups, []string{n})
		}
	}

	// Группа считается выполненной, когда успешно вызван хотя бы один её
	// инструмент. При resume уже вызванные в истории инструменты засчитываются,
	// чтобы проверка не сработала для уже продвинутого диалога.
	requiredDone := make([]bool, len(requiredGroups))
	for _, tc := range allToolCalls {
		for gi, grp := range requiredGroups {
			if requiredDone[gi] {
				continue
			}
			if slices.Contains(grp, tc.Name) {
				requiredDone[gi] = true
			}
		}
	}
	requiredAttempts := 0

	// Детекция повторяющихся вызовов (защита от зацикливания модели):
	// callCounts — сколько раз встретился конкретный вызов (имя+аргументы),
	// loopNudged — для каких вызовов уже была отправлена подсказка (не более
	// одной на сигнатуру, чтобы не спамить модель).
	callCounts := make(map[string]int)
	loopNudged := make(map[string]bool)
	// needRefills — сколько раз за цикл уже дозаправляли контекст по маркерам
	// NEED_* (защита от петли «модель просит контекст → дозаправка → снова»).
	needRefills := 0

	// pendingRequired возвращает имя первого ещё не выполненного обязательного
	// инструмента — им runner подсказывает модели в подсказках.
	pendingRequired := func() string {
		for gi, grp := range requiredGroups {
			if !requiredDone[gi] {
				return grp[0]
			}
		}
		return ""
	}

	for round := startRound; round < startRound+mx; round++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("контекст отменён до раунда %d: %w", round+1, err)
		}

		// Сжатие истории под символьный бюджет (CODEGEN_HISTORY_BUDGET): при
		// включённом бюджете перед каждым запросом выбрасываем старейшие пары
		// assistant+tool из середины, сохраняя систему, задачу и актуальный
		// хвост. Счётчики allToolCalls/requiredDone от истории не зависят.
		if budget := historyBudget(); budget > 0 {
			if compacted := CompressHistory(messages, budget); len(compacted) != len(messages) {
				Debugf("RUNNER: раунд %d: сжатие истории %d -> %d сообщений (бюджет %d)", round+1, len(messages), len(compacted), budget)
				messages = compacted
			}
		}

		reply, err := provider.ChatOnce(ctx, agent, messages)
		if err != nil {
			return nil, err
		}

		if len(reply.ToolCalls) == 0 {
			// Если не выполнена хотя бы одна группа обязательных инструментов
			// (модель ответила текстом вместо вызова ИЛИ вызов вернул ошибки),
			// а лимит подсказок не исчерпан — подсказываем и повторяем, чтобы
			// не завершить цикл без реального результата.
			if pending := pendingRequired(); pending != "" && requiredAttempts < requiredRetries {
				requiredAttempts++
				msg := nudgeMessage(pending)
				if len(allToolCalls) > 0 {
					msg = retryMessage(pending)
				}
				Debugf("RUNNER: раунд %d: обязательный инструмент %q не сработал успешно, подсказываю (%d/%d) и повторяю", round+1, pending, requiredAttempts, requiredRetries)
				messages = append(messages, Message{
					Role:    "user",
					Content: msg,
				})
				continue
			}

			content = reply.Content
			// finish_reason="length" означает, что модель упёрлась в лимит
			// токенов генерации и ответ не полный: помечаем ответ усечённым,
			// чтобы вызывающий код (например, исполнитель плана) мог отличить
			// «модель закончила» от «модель обрезалась на полуслове».
			truncated := reply.FinishReason == "length"
			if truncated {
				Debugf("RUNNER: раунд %d: модель обрезалась по лимиту токенов (finish_reason=length), content=%q", round+1, Truncate(content, 300))
			}

			// Дозаправка контекста on-demand: модель вместо завершения попросила
			// недостающие данные маркерами NEED_CONTEXT/NEED_SIGNATURE/NEED_FILE.
			// Подтягиваем компактный контекст (карта кода/интервал строк) у
			// агента и продолжаем диалог, пока не исчерпаны повторы. Так
			// модель не галлюцинирует сигнатуры соседних пакетов, а получает
			// их точечно, без чтения файлов целиком.
			if !truncated {
				if pulled, pullNeeded := needContextPull(agent, content); pullNeeded && needRefills < maxNeedRefills {
					needRefills++
					Debugf("RUNNER: раунд %d: дозаправка контекста по маркеру NEED_* (%d целей, %d/%d)", round+1, len(pulled), needRefills, maxNeedRefills)
					messages = append(messages, Message{Role: "assistant", Content: content})
					messages = append(messages, Message{Role: "user", Content: contextRefillMessage(pulled)})
					content = ""
					continue
				}
			}

			Debugf("RUNNER: раунд %d: модель завершила (finish_reason=%q), content=%q",
				round+1, reply.FinishReason, Truncate(content, 300))
			// История включает финальный ответ модели: если ответ усечён по
			// лимиту токенов, следующий resume продолжит его с этого места.
			messages = append(messages, Message{Role: "assistant", Content: content})
			return &AgentResponse{
				Content:   content,
				ToolCalls: allToolCalls,
				Truncated: truncated,
				Rounds:    round + 1,
				Messages:  messages,
			}, nil
		}

		Debugf("RUNNER: раунд %d: модель запросила %d вызова(ов), finish_reason=%q",
			round+1, len(reply.ToolCalls), reply.FinishReason)

		messages = append(messages, Message{Role: "assistant", Content: reply.Content, ToolCalls: reply.ToolCalls})
		allToolCalls = append(allToolCalls, reply.ToolCalls...)

		for _, tc := range reply.ToolCalls {
			Debugf("RUNNER: выполняю инструмент %q args=%s", tc.Name, Truncate(tc.Arguments, 500))

			args, err := tools.ParseArguments(tc.Arguments)
			if err != nil {
				return nil, fmt.Errorf("разбор аргументов инструмента %s: %w", tc.Name, err)
			}

			callCounts[callSignature(tc.Name, args)]++

			result, err := agent.CallFunction(tc.Name, args)
			if err != nil {
				Debugf("RUNNER: инструмент %q вернул ошибку: %v", tc.Name, err)
				return nil, fmt.Errorf("выполнение инструмента %s: %w", tc.Name, err)
			}

			Debugf("RUNNER: результат инструмента %q: %s", tc.Name, Truncate(string(result), 500))
			// Отмечаем успешность обязательных инструментов: ошибки/пустые
			// результаты считаются неудачей, при которой нужна повторная подсказка.
			for gi, grp := range requiredGroups {
				if requiredDone[gi] {
					continue
				}
				if slices.Contains(grp, tc.Name) && !toolResultFailed(tc.Name, result) {
					requiredDone[gi] = true
					Debugf("RUNNER: обязательный инструмент %q выполнен успешно", tc.Name)
				}
			}
			messages = append(messages, Message{
				Role:       "tool",
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				Content:    string(result),
			})
		}

		// Защита от зацикливания: если какой-то вызов был повторён
		// maxRepeatedToolCalls раз, подсказываем модели остановиться и
		// завершить работу итоговым ответом (сжигать раунды до лимита
		// впустую не нужно). Подсказка отправляется один раз на сигнатуру.
		for sig, n := range callCounts {
			if n >= maxRepeatedToolCalls && !loopNudged[sig] {
				loopNudged[sig] = true
				toolName := sig
				if i := strings.Index(toolName, " "); i >= 0 {
					toolName = toolName[:i]
				}
				Debugf("RUNNER: раунд %d: детектировал повторяющийся вызов %q (%d раз), подсказываю завершить цикл", round+1, toolName, n)
				messages = append(messages, Message{Role: "user", Content: loopMessage(toolName, n)})
				break
			}
		}
	}

	Debugf("RUNNER: достигнут лимит раундов (%d), возвращаю частичный результат", mx)
	return &AgentResponse{
		Content:   content,
		ToolCalls: allToolCalls,
		Truncated: true,
		Rounds:    startRound + mx,
		Messages:  append([]Message{}, messages...),
	}, nil
}
