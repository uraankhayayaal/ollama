package runner

import (
	"ai/agents"
	"ai/application/usecases"
	"ai/domain"
	"ai/injections"
	"ai/runevents"
	"ai/tools"
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// AgentResponse содержит ответ модели и все выполненные вызовы функций.
// Тип принадлежит домену (domain.AgentResponse), здесь — алиас.
type AgentResponse = domain.AgentResponse

// Message — нейтральное представление сообщения диалога,
// провайдеры конвертируют его в свой формат.
type Message = domain.Message

// ModelReply — ответ модели за один раунд.
type ModelReply = domain.ModelReply

// Usage — фактическое потребление токенов одного запроса к модели.
type Usage = domain.Usage

// ChatProvider, StreamChatProvider, ModelLimitsProvider — порты LLM,
// определённые в application/usecases (см. usecases/ports.go); здесь алиасы
// для обратной совместимости пакета runner.
type ChatProvider = usecases.ChatProvider

// StreamChunk — фрагмент потокового ответа модели (принадлежит домену).
type StreamChunk = domain.StreamChunk

// StreamChatProvider — опциональное потоковое расширение ChatProvider.
type StreamChatProvider = usecases.StreamChatProvider

// ModelLimits — алиас domain.ModelLimits.
type ModelLimits = domain.ModelLimits

// ModelLimitsProvider — опциональный порт лимитов модели (usecases).
type ModelLimitsProvider = usecases.ModelLimitsProvider

// EstimateTextTokens — алиас domain.EstimateTextTokens (см. domain/tokens.go).
func EstimateTextTokens(s string) int { return domain.EstimateTextTokens(s) }

// EstimateUsage — алиас domain.EstimateUsage (см. domain/tokens.go).
func EstimateUsage(messages []Message, reply *ModelReply) (in, out int) {
	return domain.EstimateUsage(messages, reply)
}

// ToolRequiringAgent — порт агента с обязательным инструментом за цикл.
type ToolRequiringAgent = usecases.ToolRequiringAgent

// RequiredToolGroupsAgent — порт агента с обязательными группами инструментов.
type RequiredToolGroupsAgent = usecases.RequiredToolGroupsAgent

// ResumeState — точка возобновления агентского цикла после лимита раундов:
// полная история диалога и количество уже потраченных раундов. Собирается
// вызывающим кодом из чекпоинта и передаётся в цикл через контекст.
type ResumeState struct {
	Messages []Message
	Rounds   int
}

// resumeStateKey — тип ключа контекста для передачи состояния resume.
type resumeStateKey struct{}

// resumeOffKey — тип ключа контекста для принудительного сброса resume
// (WithoutResumeState): задача берётся «с другой стороны», заново.
type resumeOffKey struct{}

// WithResumeState помещает состояние возобновления агентского цикла в контекст.
// Провайдеры сами не меняются: runner.Generate читает состояние из контекста.
func WithResumeState(ctx context.Context, state *ResumeState) context.Context {
	return context.WithValue(ctx, resumeStateKey{}, state)
}

// ResumeStateFromContext извлекает состояние возобновления из контекста.
func ResumeStateFromContext(ctx context.Context) *ResumeState {
	if off, _ := ctx.Value(resumeOffKey{}).(bool); off {
		return nil
	}
	if st, ok := ctx.Value(resumeStateKey{}).(*ResumeState); ok {
		return st
	}
	return nil
}

// WithoutResumeState убирает состояние возобновления из контекста: следующий
// цикл начнётся заново (свежая история, полный бюджет раундов).
//
// Нужен там, где задачу берут «с другой стороны»: продолжение того же
// застрявшего диалога после срыва воспроизводит ту же петлю.
func WithoutResumeState(ctx context.Context) context.Context {
	return context.WithValue(ctx, resumeOffKey{}, true)
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

// nudgeMessage — алиас usecases.NudgeMessage (политика цикла в application).
func nudgeMessage(toolName string) string { return usecases.NudgeMessage(toolName) }

// retryMessage — алиас usecases.RetryMessage.
func retryMessage(toolName string) string { return usecases.RetryMessage(toolName) }

// Пороговые константы политики цикла (реализация — application/usecases/policy.go).
const (
	maxRepeatedToolCalls      = usecases.MaxRepeatedToolCalls
	maxRepeatedToolFails      = usecases.MaxRepeatedToolFails
	maxNeedRefills            = usecases.MaxNeedRefills
	requiredProgressAfter     = usecases.RequiredProgressAfter
	requiredProgressEvery     = usecases.RequiredProgressEvery
	maxRequiredProgressNudges = usecases.MaxRequiredProgressNudges
)

// ContextSupplier — алиас usecases.ContextFetcher.
type ContextSupplier = usecases.ContextFetcher

// NeedContextTargets — алиас usecases.NeedContextTargets.
func NeedContextTargets(content string) []string { return usecases.NeedContextTargets(content) }

// needContextPull — алиас usecases.NeedContextPull.
func needContextPull(agent agents.Agent, content string) (map[string]string, bool) {
	return usecases.NeedContextPull(agent, content)
}

// contextRefillMessage — алиас usecases.ContextRefillMessage.
func contextRefillMessage(pulled map[string]string) string {
	return usecases.ContextRefillMessage(pulled)
}

// loopMessage — алиас usecases.LoopMessage.
func loopMessage(toolName string, n int) string { return usecases.LoopMessage(toolName, n) }

// progressNudgeMessage — алиас usecases.ProgressNudgeMessage.
func progressNudgeMessage(toolName string, round, total, nudge int) string {
	return usecases.ProgressNudgeMessage(toolName, round, total, nudge)
}

// toolFailMessage — алиас usecases.ToolFailMessage.
func toolFailMessage(toolName string, n int, required string) string {
	return usecases.ToolFailMessage(toolName, n, required)
}

// Параллельное выполнение инструментов (parallel tool calling).
//
// Модель может запросить НЕСКОЛЬКО вызовов за один раунд. Read-only
// инструменты (List/ReadFiles/ReadMap и чтение доски) независимы и безопасны
// для одновременного запуска — это заметно ускоряет цикл: главный боттлнек
// агента-разработчика/лида — последовательное чтение многих файлов. Все
// остальные инструменты выполняются строго последовательно в порядке вызова,
// сохраняя прежнюю семантику (см. tools.IsParallelSafe и tools/parallel.go).
//
// Фича включается по умолчанию, отключается PARALLEL_TOOL_CALLS=0. Ширина
// волны ограничивается PARALLEL_TOOL_MAX (0 — без ограничения).
const (
	defaultParallelMax     = 8
	parallelToolCallsOnEnv = "PARALLEL_TOOL_CALLS"
	parallelToolMaxOnEnv   = "PARALLEL_TOOL_MAX"
)

// parallelToolCallsEnabled сообщает, включено ли параллельное выполнение
// независимых (read-only) инструментов одного раунда. По умолчанию — да;
// выключается PARALLEL_TOOL_CALLS=0/false/off.
func parallelToolCallsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(parallelToolCallsOnEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// parallelToolMax — максимальная ширина параллельной волны read-only
// инструментов (PARALLEL_TOOL_MAX). 0 или отрицательное значение — без
// ограничения.
func parallelToolMax() int {
	if v := strings.TrimSpace(os.Getenv(parallelToolMaxOnEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultParallelMax
}

// toolCallJob — подготовленный вызов инструмента раунда: аргументы уже
// разобраны из JSON строки, к вызову готов.
type toolCallJob struct {
	tc   tools.ToolCall
	args map[string]any
}

// toolOutcome — результат выполнения одного вызова инструмента.
type toolOutcome struct {
	result []byte
	err    error
}

// toolResultFailed — алиас usecases.ToolResultFailed (политика в application).
func toolResultFailed(toolName string, result []byte) bool {
	return usecases.ToolResultFailed(toolName, result)
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
	// Репортёр live-событий: если контекст снабжён Router (Web UI), каждый
	// ответ модели и каждый вызов инструмента транслируются наружу.
	rep := runevents.ReporterFromContext(ctx)

	// Собираем user-сообщения (например, дифф для ревью), чтобы передать
	// их контекст в метод системных сообщений (GetSystemMessages).
	userMessages := agent.GetUserMessages()

	// messages хранит «чистый» диалог без инъекций; модель в каждом раунде
	// получает его копию с инъекциями (applyInjections ниже). Благодаря этому
	// правка инъекций видна модели со следующего раунда, а не со следующего
	// запуска агента, и resume-сегмент получает инъекции заново из контекста.
	baseMessages := []Message{}
	if resume != nil && len(resume.Messages) > 0 {
		// Возобновление после лимита раундов: история уже содержит системные
		// и user-сообщения, добавлять их повторно нельзя.
		baseMessages = append(baseMessages, resume.Messages...)
	} else {
		// Системные сообщения размещаем в начале диалога, как это принято,
		// а user-сообщения — следом. Контекст (дифф) передаётся в метод
		// системных сообщений через параметр.
		systemMessages := agent.GetSystemMessages(userMessages)

		for _, m := range systemMessages {
			baseMessages = append(baseMessages, Message{Role: "system", Content: m.Message})
		}
		for _, m := range userMessages {
			baseMessages = append(baseMessages, Message{Role: "user", Content: m.Message})
		}
	}

	// Промпт-инъекции: собираем из всех источников (global → assistant →
	// session → runtime), включая живую задачу из контекста.
	injs := newInjectionSet(ctx, agent)
	// Контекст для условий when и шаблонов инъекций.
	injCtx := injectionMergeContext(ctx, agent, provider)

	// Нейтральный ChatRequest раунда: роль, инструменты, обязательный инструмент
	// и признак тяжёлого слоя фиксируются один раз по агенту; в цикле меняется
	// только Messages. Так провайдеры не знают об agents.Agent.
	reqTools := agent.GetTools()
	reqRole := domain.AgentRole(agentRole(agent))
	var reqForceTool string
	if tr, ok := agent.(ToolRequiringAgent); ok {
		if n, yes := tr.RequiredToolFirstRound(); yes {
			reqForceTool = n
		}
	}
	reqHeavy := false
	if h, ok := agent.(interface{ NeedsHeavyModel() bool }); ok && h.NeedsHeavyModel() {
		reqHeavy = true
	}

	startRound := 0
	if resume != nil {
		startRound = resume.Rounds
	}

	// applyInjections собирает рабочее сообщение для запроса: применяет
	// инъекции к истории и логирует метаданные. Без инъекций возвращает
	// историю как есть (fast-path, обратная совместимость).
	//
	// Источники читаются ОДИН раз на запрос: живой источник ходит в доску, и
	// двойное чтение давало бы два разных снимка инъекций в одном промпте.
	applyInjections := func(history []Message, turn int) []Message {
		all := injs.collect()
		if len(all) == 0 {
			return history
		}
		ctxFor := injCtx
		ctxFor.Turn = turn + 1

		var baseSystem string
		var baseMsgs []injections.Message
		for _, m := range history {
			if m.Role == "system" {
				baseSystem = m.Content
			} else {
				baseMsgs = append(baseMsgs, injections.Message{Role: m.Role, Content: m.Content})
			}
		}
		ctxFor.System = baseSystem
		ctxFor.Messages = baseMsgs

		result, err := injections.ApplyInjections(baseSystem, baseMsgs, all, ctxFor)
		if err != nil || result == nil {
			// Ошибка пайплайна не должна ронять агентский цикл: модель получит
			// промпт без инъекций, а причина уйдёт в debug-лог.
			if err != nil {
				Debugf("RUNNER: инъекции не применены: %v", err)
			}
			return history
		}
		if applied := injections.DescribeApplied(result.Applied); applied != "" {
			Debugf("RUNNER: раунд %d: применены инъекции: %s", turn+1, applied)
		}
		if skipped := injections.DescribeSkipped(result.Skipped); skipped != "" {
			Debugf("RUNNER: раунд %d: инъекции пропущены: %s", turn+1, skipped)
		}
		if result.ReplaceWarning != "" {
			Debugf("RUNNER: раунд %d: %s", turn+1, result.ReplaceWarning)
		}

		out := make([]Message, 0, len(result.Messages)+1)
		if result.System != "" {
			out = append(out, Message{Role: "system", Content: result.System})
		}
		for _, m := range result.Messages {
			out = append(out, Message{Role: m.Role, Content: m.Content})
		}
		return out
	}

	// messages — чистая история сегмента; инъекции добавляются в request
	// по раундам (см. applyInjections и вызов внутри цикла).
	messages := baseMessages

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
	// lastInputTokens — фактический вход (токены) последнего запроса к модели:
	// используется для ужесточения сжатия (Ф-11, CODEGEN_HISTORY_TOKENS).
	lastInputTokens := 0

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
	// Детекция повторяющихся ПРОВАЛОВ инструмента: failCounts — сколько раз
	// инструмент вернул ошибку за цикл (имя без аргументов), loopFailNudged —
	// для каких имён уже подсказали. Ловит перебор аргументов в цикле ошибок.
	failCounts := make(map[string]int)
	loopFailNudged := make(map[string]bool)
	// Детектор зацикливания (loop.go) — жёсткая реакция на петлю: разрыв
	// зацикленного хвоста истории с дайджестом «иди с другой стороны», а при
	// повторе запрещённого — приговор Looped вместо сжигания бюджета раундов.
	// Мягкие подсказки выше остаются как первый, дешёвый уровень помощи.
	loop := usecases.NewLoopDetector(tools.IsParallelSafe)
	// loopPrefix — число ведущих сообщений с постановкой задачи: разрыв петли
	// их не трогает, даже если петля началась на первом раунде.
	loopPrefix := usecases.LoopPrefixLen(messages)
	// loopProgressIdx — индекс сообщения, где цикл последний раз делал что-то
	// новое. Разрыв схлопывает всё после него: модель перестаёт видеть паттерн,
	// который повторяет.
	loopProgressIdx := loopPrefix
	// loopTask — постановка задачи (пользовательские сообщения префикса). После
	// разрыва петли она печатается заново последним сообщением: заставляем
	// модель вернуться к заданию, а не продолжать «по памяти».
	loopTask := usecases.LoopTaskStatement(messages[:loopPrefix])
	// needRefills — сколько раз за цикл уже дозаправляли контекст по маркерам
	// NEED_* (защита от петли «модель просит контекст → дозаправка → снова»).
	needRefills := 0
	// autoFixUsed — сколько подряд итераций авто-самоисправления (Ф-2) уже
	// подмешано в этом эпизоде. Сбрасывается, когда раунд мутаций прошёл без
	// диагностик; ограничен autoFixMaxRounds().
	autoFixUsed := 0
	// sentDiags — диагностики, уже показанные модели в этом эпизоде: повторные
	// строки не дублируются (токен-бюджет, Ф-4). Сбрасывается на чистом раунде.
	sentDiags := make(map[string]bool)
	// appLogFeedOn/appLogFeedUsed/sentAppLogs — то же для логов рантайма (Ф-2):
	// подмешивания ограничены, уже показанные строки не дублируются.
	appLogFeedOn := appLogFeedEnabled()
	appLogFeedUsed := 0
	sentAppLogs := make(map[string]bool)
	// progressNudges — сколько раз за цикл модели уже напомнили о невыполненном
	// обязательном действии (подсказки по раундам, см. requiredProgressAfter).
	progressNudges := 0

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

		// Сжатие истории (Ф-6..Ф-11): бюджет из CODEGEN_HISTORY_BUDGET и
		// ужесточение по фактическому входу прошлого раунда (Ф-11,
		// CODEGEN_HISTORY_TOKENS), плюс побочные фичи из CompressionClient
		// контекста — RAG-вытеснение (Ф-7), ранжирование (Ф-8), LSP-оглавления
		// (Ф-9), компакция (Ф-10) и памятка (Ф-6). Счётчики
		// allToolCalls/requiredDone от истории не зависят.
		if compacted, _ := compressHistoryForRound(ctx, provider, messages, lastInputTokens); len(compacted) != len(messages) {
			messages = compacted
		}

		// Принудительное вмешательство детектора петли уходит в ЭТОТ запрос.
		// Порядок принципиален: предупреждение приготовлено в прошлом раунде по
		// наблюдению «проверка падает, состояние не меняется», и должно быть видно
		// модели немедленно — иначе оно доезжает на 1–2 раунда позже, а к этому
		// моменту разрыв петли уже схлопнул историю и предупреждение пропало бы
		// вместе с ней. После сжатия истории, чтобы компрессор его не вырезал.
		if injection := loop.TakeInjection(); injection != "" {
			Debugf("RUNNER: раунд %d: вмешательство детектора петли внедрено в запрос (%d символов)", round+1, len(injection))
			messages = append(messages, Message{Role: "user", Content: injection})
			if rep != nil {
				rep.OnMessage("user", injection, false)
			}
		}

		// Промпт-инъекции применяются именно к ЭТОМУ запросу: messages хранит
		// «чистый» диалог (без инъекций), а модель получает request — ту же
		// историю с инъекциями. Поэтому правка инъекций (доска/сессия) видна
		// модели со следующего раунда, а не со следующего запуска агента, и
		// resume-сегмент получает инъекции заново, из контекста.
		request := applyInjections(messages, round)

		// Если провайдер поддерживает стриминг — текст отдаём по кускам
		// (Reporter.OnMessageDelta), иначе fallback на разовый ChatOnce.
		// Финальное OnMessage с полным текстом приходит в обоих случаях ниже.
		var reply *ModelReply
		var err error
		chatReq := domain.ChatRequest{
			Role:          reqRole,
			Messages:      request,
			Tools:         reqTools,
			ForceToolName: reqForceTool,
			Heavy:         reqHeavy,
		}
		if sp, ok := provider.(StreamChatProvider); ok {
			streamID := fmt.Sprintf("stream-%d", round+1)
			reply, err = sp.ChatStream(ctx, chatReq, func(ch StreamChunk) {
				if rep != nil && ch.Partial != "" {
					rep.OnMessageDelta(streamID, ch.Partial)
				}
			})
		} else {
			reply, err = provider.ChatOnce(ctx, chatReq)
		}
		if err != nil {
			return nil, err
		}

		// Счётчик токенов: предпочитаем фактический usage провайдера, иначе —
		// эвристическую оценку по истории и ответу. Событие уходит в репортёр
		// (Web UI) для живой трансляции в шину проекта.
		in, out := EstimateUsage(request, reply)
		var tps float64
		if u := reply.Usage; u != nil {
			if u.InputTokens > 0 {
				in = u.InputTokens
			}
			if u.OutputTokens > 0 {
				out = u.OutputTokens
			}
			// Реальная скорость генерации, когда провайдер её сообщает.
			tps = u.OutputTPS
		}
		if rep != nil && (in > 0 || out > 0) {
			rep.OnTokens(int64(in), int64(out), tps)
		}
		// Фактический вход пишем в счётчик — следующий раунд сожмётся жёстче,
		// если провайдер перенёс запрос больше лимита (Ф-11).
		lastInputTokens = in

		if rep != nil {
			rep.OnMessage("assistant", reply.Content, reply.FinishReason == "length")
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

		// Готовим вызовы: разбираем аргументы и помечаем read-only инструменты,
		// которые можно выполнить параллельно (независимые, без аргументов от
		// результатов других вызовов).
		jobs := make([]toolCallJob, len(reply.ToolCalls))
		parallel := make([]bool, len(reply.ToolCalls))
		badArgs := make([]string, len(reply.ToolCalls))
		nParallel := 0
		for i, tc := range reply.ToolCalls {
			Debugf("RUNNER: выполняю инструмент %q args=%s", tc.Name, Truncate(tc.Arguments, 500))

			args, err := tools.ParseArguments(tc.Arguments)
			if err != nil {
				// Дефект JSON от модели не должен убивать оркестрацию: раньше
				// здесь запуск падал с «invalid character ']' looking for
				// beginning of value» и вся сессия терялась. Вместо этого
				// вызов помечается как невыполненный, а модель получает
				// требование повторить его с валидным JSON.
				badArgs[i] = fmt.Sprintf("Аргументы инструмента %q пришли невалидным JSON (%s), поэтому вызов НЕ выполнен. "+
					"Повтори вызов с корректным JSON: двойные кавычки, без висячих запятых, переводы строк внутри строк — как \n.",
					tc.Name, Truncate(err.Error(), 160))
				Debugf("RUNNER: аргументы инструмента %q не разобрались: %v", tc.Name, err)
			}

			jobs[i] = toolCallJob{tc: tc, args: args}
			if len(reply.ToolCalls) > 1 && parallelToolCallsEnabled() && tools.IsParallelSafe(tc.Name) {
				parallel[i] = true
				nParallel++
			}
		}

		outcomes := make([]toolOutcome, len(jobs))

		// Волна параллельных (read-only) вызовов: одно соединение к провайдеру
		// уже потрачено, поэтому выигрыш — в одновременном выполнении чтений.
		// Результаты собираются по индексу, порядок tool-сообщений ниже
		// восстанавливается по порядку вызова в исходном сообщении модели.
		if nParallel > 0 {
			limit := parallelToolMax()
			if limit < 1 {
				limit = len(jobs)
			}
			Debugf("RUNNER: раунд %d: запускаю %d read-only вызовов параллельно (лимит волны %d)", round+1, nParallel, limit)

			sem := make(chan struct{}, limit)
			var wg sync.WaitGroup
			for i := range jobs {
				if !parallel[i] {
					continue
				}
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()

					j := jobs[i]
					if rep != nil {
						rep.OnToolStart(j.tc.Name, Truncate(j.tc.Arguments, 2000))
					}
					result, err := agent.CallFunction(j.tc.Name, j.args)
					outcomes[i] = toolOutcome{result: result, err: err}
					if rep != nil {
						rep.OnToolResult(j.tc.Name, Truncate(string(result), 8000), err == nil && !toolResultFailed(j.tc.Name, result))
					}
				}(i)
			}
			wg.Wait()

			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("контекст отменён во время выполнения инструментов: %w", err)
			}
		}

		// Последовательный проход в порядке вызова: read-only вызовы берут уже
		// готовый результат волны, остальные выполняются как раньше — по одному.
		// Учёт в callCounts/failCounts/requiredDone и история диалога идентичны
		// последовательному исполнению; порядок tool-сообщений = порядок вызова.
		var loopCalls []usecases.LoopRoundCall
		roundProgress := false
		for i, j := range jobs {
			var (
				result  []byte
				err     error
				blocked bool
			)
			sig := usecases.CallSignature(j.tc.Name, j.args)
			switch {
			case badArgs[i] != "":
				// Модель прислала невалидный JSON аргументов: вызов не
				// выполняем, но и запуск не роняем.
				result = usecases.ToolErrorResult(badArgs[i])
			case parallel[i]:
				result, err = outcomes[i].result, outcomes[i].err
			default:
				// Verify guard: проверка, которая падала слишком много раз подряд,
				// не запускается повторно — раунд сгорал бы впустую. Блокировка по
				// нормализованной проверке, поэтому смена аргументов (строки, grep,
				// --reporter) её не обходит.
				if reason, isBlocked := loop.BlockedVerifyCall(j.tc.Name, j.args); isBlocked {
					blocked = true
					result = usecases.ToolErrorResult(reason)
					Debugf("RUNNER: раунд %d: вызов %q заблокирован verify guard: %s", round+1, j.tc.Name, reason)
					if rep != nil {
						rep.OnToolResult(j.tc.Name, Truncate(string(result), 8000), false)
					}
					break
				}
				if rep != nil {
					rep.OnToolStart(j.tc.Name, Truncate(j.tc.Arguments, 2000))
				}
				result, err = agent.CallFunction(j.tc.Name, j.args)
				if rep != nil {
					rep.OnToolResult(j.tc.Name, Truncate(string(result), 8000), !toolResultFailed(j.tc.Name, result))
				}
			}

			if err != nil {
				Debugf("RUNNER: инструмент %q вернул ошибку: %v", j.tc.Name, err)
				return nil, fmt.Errorf("выполнение инструмента %s: %w", j.tc.Name, err)
			}

			// Вывод любого инструмента нормализуется ДО попадания в историю
			// диалога: escape-последовательности терминала и управляющие байты
			// деформируют JSON-пакеты и роняют разбор аргументов на следующих
			// раундах (см. runner/sanitize.go).
			result = []byte(domain.SanitizeToolOutput(string(result)))

			callCounts[sig]++

			Debugf("RUNNER: результат инструмента %q: %s", j.tc.Name, Truncate(string(result), 500))
			failed := toolResultFailed(j.tc.Name, result)
			state, stateSample, stateLabel := usecases.LoopStateFromCall(j.tc.Name, j.args, result)
			vcmd := usecases.VerifyCommandOf(j.tc.Name, j.args)
			loopCalls = append(loopCalls, usecases.LoopRoundCall{
				Name:        j.tc.Name,
				Sig:         sig,
				Failed:      failed,
				Result:      Truncate(string(result), usecases.LoopDigestResult),
				Paths:       usecases.LoopCallPaths(j.args),
				State:       state,
				StateSample: stateSample,
				StateLabel:  stateLabel,
				Verify:      vcmd != "" && usecases.LoopIsVerificationCommand(vcmd),
				VerifyCmd:   vcmd,
				Blocked:     blocked,
			})
			if !failed {
				roundProgress = true
			}
			// Считаем провалы инструмента НЕЗАВИСИМО от аргументов: если модель
			// «перебирает» аргументы в цикле ошибок, сигнатурный определитель
			// её не ловит, а счётчик по имени — ловит.
			if failed {
				failCounts[j.tc.Name]++
			}
			// Отмечаем успешность обязательных инструментов: ошибки/пустые
			// результаты считаются неудачей, при которой нужна повторная подсказка.
			for gi, grp := range requiredGroups {
				if requiredDone[gi] {
					continue
				}
				if slices.Contains(grp, j.tc.Name) && !failed {
					requiredDone[gi] = true
					Debugf("RUNNER: обязательный инструмент %q выполнен успешно", j.tc.Name)
				}
			}
			messages = append(messages, Message{
				Role:       "tool",
				ToolName:   j.tc.Name,
				ToolCallID: j.tc.ID,
				Content:    string(result),
			})

			// Модель повторила вызов, который разрыв петли прямо запретил:
			// вежливое предупреждение не сработало — возвращаем приговор, чтобы
			// вызывающий код взял задачу с другой стороны (другая модель или
			// честный фейл), вместо того чтобы жечь остаток бюджета раундов.
			if loop.BannedSignature(sig, j.tc.Name) && loop.RegisterStrike() {
				reason := fmt.Sprintf("после предупреждения о петле модель повторила запрещённый вызов %s (%d-й раз)", usecases.SignatureLabel(sig), loop.Strikes())
				Debugf("RUNNER: раунд %d: %s — цикл прерван (Looped)", round+1, reason)
				if rep != nil {
					rep.OnMessage("user", "Цикл агента прерван: "+reason, false)
				}
				return &AgentResponse{
					Content:    content,
					ToolCalls:  allToolCalls,
					Looped:     true,
					LoopReason: reason,
					Rounds:     round + 1,
					Messages:   append([]Message{}, messages...),
				}, nil
			}
		}

		// Ф-2 + Ф-5 + Ф-6: хуки после раунда с мутациями. Очередь затронутых
		// файлов дренится ОДИН раз (TakeTouched) и раздаётся активным хукам:
		// авто-самоисправление LSP (Ф-2), частичная переиндексация RAG (Ф-5) и
		// промежуточный коммит правок (Ф-6).
		// Хук активен, когда включён ЛЮБОЙ из режимов (LSP_AUTO_FIX,
		// RAG_AUTO_REINDEX, KANBAN_WIP_COMMIT). События в Web UI — как у
		// обычного инструмента.
		autofixOn := autoFixEnabled()
		reindexOn := reindexEnabled()
		wipOn := wipCommitEnabled()
		// Результаты раунда для State Tracking (Ф-6, этап 2): что менял агент и
		// чем закончился промежуточный коммит. Вне блока хуков, потому что
		// состояние задачи пишется каждый раунд, даже когда файлы не менялись
		// (агент гоняет проверки).
		var roundTouched []string
		var roundSHA string
		var roundCommitted bool
		if af, ok := agent.(AutoFixer); ok && (autofixOn || reindexOn || wipOn) {
			roundTouched = af.TakeTouched()
			touched := roundTouched

			// Ф-6: промежуточный коммит раунда — единица ручного отката задачи.
			// Идёт ПЕРВЫМ, до авто-лечения и реиндексации, чтобы в коммит
			// попали ровно те файлы, которые агент изменил в этом раунде.
			if wipOn && len(touched) > 0 {
				if wc, ok := agent.(WIPCommitter); ok {
					if rep != nil {
						rep.OnToolStart("WipCommit", "промежуточный коммит правок раунда")
					}
					sha, committed, cerr := wc.CommitRoundTouched(touched, round+1)
					roundSHA, roundCommitted = sha, committed
					switch {
					case cerr != nil:
						// Коммит — вспомогательная фиксация: недоступный git не
						// должен ронять генерацию (деградируем в лог, как RAG).
						if rep != nil {
							rep.OnToolResult("WipCommit", Truncate("коммит не выполнен: "+cerr.Error(), 2000), false)
						}
						Debugf("RUNNER: раунд %d: промежуточный коммит: %v", round+1, cerr)
					case committed:
						if rep != nil {
							rep.OnToolResult("WipCommit", Truncate("зафиксировано раундом "+strconv.Itoa(round+1)+": "+sha, 2000), true)
						}
						Debugf("RUNNER: раунд %d: промежуточный коммит %s (%d файлов)", round+1, sha, len(touched))
					default:
						if rep != nil {
							rep.OnToolResult("WipCommit", "дерево чистое, коммит не создавался", true)
						}
					}
				}
			}

			// Ф-5: частичная переиндексация RAG. Промптов модели не подмешивает —
			// обновляет векторную память, чтобы последующие CodeSearch/контекст
			// плана видели свежий код. Деградирует тихо при недоступном Qdrant.
			if reindexOn && len(touched) > 0 {
				if ri, ok := agent.(Reindexer); ok {
					if rep != nil {
						rep.OnToolStart("RAGReindex", "переиндексация файлов, затронутых раундом")
					}
					n, rerr := ri.ReindexTouched(touched)
					if rep != nil {
						rep.OnToolResult("RAGReindex", Truncate(fmt.Sprintf("переиндексировано файлов: %d", n), 8000), rerr == nil)
					}
					if rerr != nil {
						Debugf("RUNNER: раунд %d: переиндексация RAG: %v", round+1, rerr)
					}
				}
			}

			// Ф-2: авто-самоисправление. Проверяем затронутые файлы через
			// LspCheck и, если есть ошибки, подмешиваем СКРЫТЫЙ user-промпт
			// с точными строками — модель правит код, не жгя раунды на
			// перечитывание сырых логов.
			if autofixOn {
				if rep != nil {
					rep.OnToolStart("LspAutoFix", "диагностика файлов, затронутых раундом")
				}
				diags, hadMutation := af.LspCheckFiles(touched)
				fresh := filterNewDiags(diags, sentDiags)
				if rep != nil {
					rep.OnToolResult("LspAutoFix", Truncate(strings.Join(diags, "\n"), 8000), !hadMutation || len(diags) == 0)
				}
				switch {
				case !hadMutation:
					// Ничего не менялось (только чтения) — проверять нечего.
				case len(diags) == 0:
					// Раунд мутаций без ошибок: эпизод закрыт, счётчик итераций
					// сбрасываем (следующая поломка снова получит полный лимит),
					// а память об отправленном — очищаем.
					autoFixUsed = 0
					sentDiags = make(map[string]bool)
				case len(fresh) == 0:
					// Все диагностики уже показаны модели ранее — не дублируем
					// (токен-бюджет, Ф-4); подсказку не подмешиваем.
					Debugf("RUNNER: раунд %d: авто-лечение: все %d диагностик уже отправлены, подсказка не нужна", round+1, len(diags))
				case autoFixUsed >= autoFixMaxRounds():
					Debugf("RUNNER: раунд %d: авто-лечение: лимит итераций (%d) исчерпан, подсказки прекращены", round+1, autoFixUsed)
				default:
					autoFixUsed++
					for _, d := range fresh {
						sentDiags[d] = true
					}
					Debugf("RUNNER: раунд %d: авто-лечение: подмешиваю подсказку с %d диагностиками (%d/%d)", round+1, len(fresh), autoFixUsed, autoFixMaxRounds())
					messages = append(messages, Message{Role: "user", Content: autoFixMessage(fresh, autoFixUsed, autoFixMaxRounds())})
				}
			}
		}

		// Ф-6 (этап 2): State Tracking. Агент, умеющий писать состояние на доску
		// (RoundStateReporter), узнаёт, чем занят раунд: проверка упала / идёт /
		// пишет код, плюс SHA промежуточного коммита — точку, к которой потом
		// вернёт ручной откат.
		if rs, ok := agent.(RoundStateReporter); ok {
			rs.ReportRoundState(newRoundState(round+1, loopCalls, roundTouched, roundSHA, roundCommitted))
		}

		// Ф-2: логи рантайма в цикл самокоррекции. Хук НЕ зависит от того,
		// менялись ли файлы: приложение могло упасть и без мутаций в этом
		// раунде (например, разработчик только что прочитал логи и теперь
		// должен на них отреагировать).
		if appLogFeedOn {
			if rl, ok := agent.(RuntimeLogger); ok {
				source, rawLines := rl.TakeAppLogTail()
				fresh := filterNewDiags(trimAppLogLines(rawLines), sentAppLogs)
				switch {
				case len(rawLines) == 0:
					// Логов в этом раунде не было — нечего подмешивать.
				case len(fresh) == 0:
					// Те же строки уже показаны: повтор только жёг бы раунд.
					Debugf("RUNNER: раунд %d: логи рантайма уже отправлены (%d строк), повтор пропущен", round+1, len(rawLines))
				case appLogFeedUsed >= appLogMaxFeedRounds():
					Debugf("RUNNER: раунд %d: лимит подмешиваний логов рантайма (%d) исчерпан", round+1, appLogFeedUsed)
				default:
					appLogFeedUsed++
					for _, l := range fresh {
						sentAppLogs[l] = true
					}
					Debugf("RUNNER: раунд %d: подмешиваю %d строк логов рантайма (%d/%d)", round+1, len(fresh), appLogFeedUsed, appLogMaxFeedRounds())
					messages = append(messages, Message{Role: "user", Content: appLogFeedMessage(source, fresh, appLogFeedUsed, appLogMaxFeedRounds())})
				}
			}
		}

		// Раунд сделал что-то новое — запоминаем точку, до которой история
		// реально полезна: разрыв петли схлопнет только хвост после неё.
		if roundProgress {
			loopProgressIdx = len(messages)
		}

		// Детектор зацикливания (жёсткий уровень, после мягких подсказок
		// ниже): по объективным счётчикам — повторы одного вызова, серия
		// раундов без успеха, отсутствие прогресса, только чтение, неподвижное
		// состояние — петля подтверждена. Модель, не понявшая свою ошибку,
		// игнорирует подсказки, поэтому действие одно: разрыв петли с
		// требованием идти с другой стороны. Если разрывы исчерпаны (или модель
		// повторила запрещённое) — приговор Looped: задачу должен взять другой
		// (см. AgentResponse.Looped). Предупреждение о неподвижной проверке
		// (stateWarning) приходит в запрос на следующем раунде — см. выше.
		// Срок действия временных блокировок проверок истекает в конце раунда, в
		// котором они были выставлены (блок живёт ровно verifyBlockRounds).
		loop.ExpireBlocks()

		if reason := loop.ObserveRound(loopCalls); reason != "" {
			if !loop.CanBreak() {
				Debugf("RUNNER: раунд %d: петля подтверждена (%s), разрывы исчерпаны (%d) — цикл прерван (Looped)", round+1, reason, loop.Breaks())
				if rep != nil {
					rep.OnMessage("user", "Цикл агента прерван: "+reason, false)
				}
				return &AgentResponse{
					Content:    content,
					ToolCalls:  allToolCalls,
					Looped:     true,
					LoopReason: reason,
					Rounds:     round + 1,
					Messages:   append([]Message{}, messages...),
				}, nil
			}
			digest, banned := loop.BreakLoop(reason, pendingRequired())
			Debugf("RUNNER: раунд %d: детектировано зацикливание (%s) — разрыв петли %d/%d, под запретом %d вызов(ов)",
				round+1, reason, loop.Breaks(), loop.MaxBreaks(), banned)
			messages = usecases.LoopCollapsedMessages(messages, loopProgressIdx, loopPrefix)
			// К дайджесту добавляем перезапуск: постановка задачи печатается
			// заново последним сообщением, а уже тронутые файлы предлагается
			// перечитать. Только запрет «не повторяй» без этого оставлял модели
			// право продолжать по памяти, не сверившись с заданием и кодом.
			digest += usecases.LoopRestartMessage(loopTask, loop.TouchedFiles(), pendingRequired())
			messages = append(messages, Message{Role: "user", Content: digest})
			if rep != nil {
				rep.OnMessage("user", digest, false)
			}
			// После разрыва пороги считаются заново, иначе первая же попытка
			// повторить запрещённое тут же объявила бы новую петлю.
			loopProgressIdx = usecases.LoopPrefixLen(messages)
			callCounts = make(map[string]int)
			failCounts = make(map[string]int)
			loopNudged = make(map[string]bool)
			loopFailNudged = make(map[string]bool)
			continue
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

		// Защита от зацикливания на ОШИБКАХ: если инструмент много раз подряд
		// (maxRepeatedToolFails) вернул ошибку — даже с разными аргументами —
		// подсказываем модели переключиться и (если есть) выполнить
		// невыполненное обязательное действие. Одна подсказка на имя.
		if len(failCounts) > 0 {
			for name, n := range failCounts {
				if n >= maxRepeatedToolFails && !loopFailNudged[name] {
					loopFailNudged[name] = true
					Debugf("RUNNER: раунд %d: инструмент %q вернул ошибки подряд (%d раз), подсказываю не перебирать их", round+1, name, n)
					messages = append(messages, Message{Role: "user", Content: toolFailMessage(name, n, pendingRequired())})
					break
				}
			}
		}

		// Обязательное действие так и не выполнено, а раунды уходят на
		// исследование РАЗНЫМИ вызовами (детекторы повтора/провалов молчат):
		// возвращаем модель к публикации результата. Без этого цикл догорает до
		// лимита раундов, шаг падает «исчерпан лимит раундов», а доска остаётся
		// пустой (например, лид не создал задач эпика — оркестрация стоит).
		if pending := pendingRequired(); pending != "" && progressNudges < maxRequiredProgressNudges {
			done := round - startRound + 1
			if done >= requiredProgressAfter && (done-requiredProgressAfter)%requiredProgressEvery == 0 {
				progressNudges++
				Debugf("RUNNER: раунд %d: обязательное действие %q не выполнено (%d раундов), подсказываю перейти к публикации (%d/%d)",
					round+1, pending, done, progressNudges, maxRequiredProgressNudges)
				messages = append(messages, Message{
					Role:    "user",
					Content: progressNudgeMessage(pending, round+1, startRound+mx, progressNudges),
				})
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
