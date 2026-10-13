package usecases

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"ai/domain"
)

// requiredRetries — сколько раз переспрашиваем модель, если она не вызвала
// обязательный инструмент первого раунда и ответила текстом.
const RequiredRetries = 2

// ResumeState — точка возобновления агентского цикла после лимита раундов:
// полная история диалога и количество уже потраченных раундов.
type ResumeState struct {
	Messages []domain.Message
	Rounds   int
}

// InjectionApplier применяет промпт-инъекции к истории диалога раунда.
// Реализуется инфраструктурой (runner) поверх injections/board.
type InjectionApplier interface {
	Apply(history []domain.Message, turn int) []domain.Message
}

// HistoryCompressor сжимает историю раунда под бюджет контекста. Реализуется
// инфраструктурой (runner) поверх сжатия/RAG/LSP.
type HistoryCompressor interface {
	Compress(ctx context.Context, provider ChatProvider, messages []domain.Message, lastInputTokens int) ([]domain.Message, *CompressionReport)
}

// HistoryCompressorFunc — адаптер функции к интерфейсу HistoryCompressor.
type HistoryCompressorFunc func(ctx context.Context, provider ChatProvider, messages []domain.Message, lastInputTokens int) ([]domain.Message, *CompressionReport)

func (f HistoryCompressorFunc) Compress(ctx context.Context, provider ChatProvider, messages []domain.Message, lastInputTokens int) ([]domain.Message, *CompressionReport) {
	return f(ctx, provider, messages, lastInputTokens)
}

// RunConfig — внешняя конфигурация цикла (env/инфраструктура). Порты-функции
// позволяют циклу не зависеть от tools и board.
type RunConfig struct {
	MaxRounds       int
	ParallelEnabled bool
	ParallelMax     int
	IsParallelSafe  func(name string) bool
	ParseArguments  func(raw string) (map[string]any, error)

	AutoFixEnabled      bool
	AutoFixMaxRounds    int
	ReindexEnabled      bool
	WIPEnabled          bool
	AppLogFeedEnabled   bool
	AppLogMaxFeedRounds int

	// BuildRoundState собирает domain.RoundState раунда (зависит от board,
	// поэтому остаётся в инфраструктуре).
	BuildRoundState func(round int, calls []LoopRoundCall, touched []string, sha string, committed bool) domain.RoundState
}

// RunPorts — зависимости запуска агентского цикла.
type RunPorts struct {
	Provider   ChatProvider
	Agent      Agent
	Role       domain.AgentRole
	Reporter   Reporter
	Injections InjectionApplier
	Compressor HistoryCompressor
	Resume     *ResumeState
	Config     RunConfig
}

// RunTask выполняет агентский цикл: отправляет диалог модели, исполняет
// запрошенные инструменты, возвращает результат модели обратно в историю
// и повторяет до завершения, лимита раундов или обнаружения зацикливания.
func RunTask(ctx context.Context, p RunPorts) (*domain.AgentResponse, error) {
	rep := p.Reporter
	agent := p.Agent
	resume := p.Resume

	userMessages := agent.GetUserMessages()

	baseMessages := []domain.Message{}
	if resume != nil && len(resume.Messages) > 0 {
		baseMessages = append(baseMessages, resume.Messages...)
	} else {
		systemMessages := agent.GetSystemMessages(userMessages)
		for _, m := range systemMessages {
			baseMessages = append(baseMessages, domain.Message{Role: "system", Content: m.Message})
		}
		for _, m := range userMessages {
			baseMessages = append(baseMessages, domain.Message{Role: "user", Content: m.Message})
		}
	}

	reqTools := agent.GetTools()
	var reqForceTool string
	if tr, ok := agent.(ToolRequiringAgent); ok {
		if n, yes := tr.RequiredToolFirstRound(); yes {
			reqForceTool = n
		}
	}
	var reqHeavy bool
	if h, ok := agent.(interface{ NeedsHeavyModel() bool }); ok && h.NeedsHeavyModel() {
		reqHeavy = true
	}

	startRound := 0
	if resume != nil {
		startRound = resume.Rounds
	}

	// messages — чистая история сегмента; инъекции добавляются в request по
	// раундам (Injections.Apply внутри цикла).
	messages := baseMessages

	var allToolCalls []domain.ToolCall
	for _, m := range messages {
		if m.Role == "assistant" {
			allToolCalls = append(allToolCalls, m.ToolCalls...)
		}
	}
	content := ""
	mx := p.Config.MaxRounds
	lastInputTokens := 0

	var requiredGroups [][]string
	if rg, ok := agent.(RequiredToolGroupsAgent); ok {
		requiredGroups = rg.RequiredToolGroups()
	}
	if req, ok := agent.(ToolRequiringAgent); ok {
		if n, yes := req.RequiredToolFirstRound(); yes && n != "" {
			requiredGroups = append(requiredGroups, []string{n})
		}
	}

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

	callCounts := make(map[string]int)
	loopNudged := make(map[string]bool)
	failCounts := make(map[string]int)
	loopFailNudged := make(map[string]bool)
	loop := NewLoopDetector(p.Config.IsParallelSafe)
	loopPrefix := LoopPrefixLen(messages)
	if loopPrefix > len(messages) {
		// История короче минимального «хвоста» разрыва петли (агент без
		// системных сообщений): не срезаем за пределы среза.
		loopPrefix = len(messages)
	}
	loopProgressIdx := loopPrefix
	loopTask := LoopTaskStatement(messages[:loopPrefix])
	needRefills := 0
	autoFixUsed := 0
	sentDiags := make(map[string]bool)
	appLogFeedOn := p.Config.AppLogFeedEnabled
	appLogFeedUsed := 0
	sentAppLogs := make(map[string]bool)
	progressNudges := 0

	pendingRequired := func() string {
		for gi, grp := range requiredGroups {
			if !requiredDone[gi] {
				return grp[0]
			}
		}
		return ""
	}

	applyInjections := func(history []domain.Message, turn int) []domain.Message {
		if p.Injections == nil {
			return history
		}
		return p.Injections.Apply(history, turn)
	}

	for round := startRound; round < startRound+mx; round++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("контекст отменён до раунда %d: %w", round+1, err)
		}

		if p.Compressor != nil {
			if compacted, _ := p.Compressor.Compress(ctx, p.Provider, messages, lastInputTokens); len(compacted) != len(messages) {
				messages = compacted
			}
		}

		if injection := loop.TakeInjection(); injection != "" {
			Debugf("RUNNER: раунд %d: вмешательство детектора петли внедрено в запрос (%d символов)", round+1, len(injection))
			messages = append(messages, domain.Message{Role: "user", Content: injection})
			if rep != nil {
				rep.OnMessage("user", injection, false)
			}
		}

		request := applyInjections(messages, round)

		var reply *domain.ModelReply
		var err error
		chatReq := domain.ChatRequest{
			Role:          p.Role,
			Messages:      request,
			Tools:         reqTools,
			ForceToolName: reqForceTool,
			Heavy:         reqHeavy,
		}
		if sp, ok := p.Provider.(StreamChatProvider); ok {
			streamID := fmt.Sprintf("stream-%d", round+1)
			reply, err = sp.ChatStream(ctx, chatReq, func(ch domain.StreamChunk) {
				if rep != nil && ch.Partial != "" {
					rep.OnMessageDelta(streamID, ch.Partial)
				}
			})
		} else {
			reply, err = p.Provider.ChatOnce(ctx, chatReq)
		}
		if err != nil {
			return nil, err
		}

		in, out := domain.EstimateUsage(request, reply)
		var tps float64
		if u := reply.Usage; u != nil {
			if u.InputTokens > 0 {
				in = u.InputTokens
			}
			if u.OutputTokens > 0 {
				out = u.OutputTokens
			}
			tps = u.OutputTPS
		}
		if rep != nil && (in > 0 || out > 0) {
			rep.OnTokens(int64(in), int64(out), tps)
		}
		lastInputTokens = in

		if rep != nil {
			rep.OnMessage("assistant", reply.Content, reply.FinishReason == "length")
		}

		if len(reply.ToolCalls) == 0 {
			if pending := pendingRequired(); pending != "" && requiredAttempts < RequiredRetries {
				requiredAttempts++
				msg := NudgeMessage(pending)
				if len(allToolCalls) > 0 {
					msg = RetryMessage(pending)
				}
				Debugf("RUNNER: раунд %d: обязательный инструмент %q не сработал успешно, подсказываю (%d/%d) и повторяю", round+1, pending, requiredAttempts, RequiredRetries)
				messages = append(messages, domain.Message{Role: "user", Content: msg})
				continue
			}

			content = reply.Content
			truncated := reply.FinishReason == "length"
			if truncated {
				Debugf("RUNNER: раунд %d: модель обрезалась по лимиту токенов (finish_reason=length), content=%q", round+1, truncate(content, 300))
			}

			if !truncated {
				if pulled, pullNeeded := NeedContextPull(agent, content); pullNeeded && needRefills < MaxNeedRefills {
					needRefills++
					Debugf("RUNNER: раунд %d: дозаправка контекста по маркеру NEED_* (%d целей, %d/%d)", round+1, len(pulled), needRefills, MaxNeedRefills)
					messages = append(messages, domain.Message{Role: "assistant", Content: content})
					messages = append(messages, domain.Message{Role: "user", Content: ContextRefillMessage(pulled)})
					content = ""
					continue
				}
			}

			Debugf("RUNNER: раунд %d: модель завершила (finish_reason=%q), content=%q",
				round+1, reply.FinishReason, truncate(content, 300))
			messages = append(messages, domain.Message{Role: "assistant", Content: content})
			return &domain.AgentResponse{
				Content:   content,
				ToolCalls: allToolCalls,
				Truncated: truncated,
				Rounds:    round + 1,
				Messages:  messages,
			}, nil
		}

		Debugf("RUNNER: раунд %d: модель запросила %d вызова(ов), finish_reason=%q",
			round+1, len(reply.ToolCalls), reply.FinishReason)

		messages = append(messages, domain.Message{Role: "assistant", Content: reply.Content, ToolCalls: reply.ToolCalls})
		allToolCalls = append(allToolCalls, reply.ToolCalls...)

		jobs := make([]toolCallJob, len(reply.ToolCalls))
		parallel := make([]bool, len(reply.ToolCalls))
		badArgs := make([]string, len(reply.ToolCalls))
		nParallel := 0
		for i, tc := range reply.ToolCalls {
			Debugf("RUNNER: выполняю инструмент %q args=%s", tc.Name, truncate(tc.Arguments, 500))

			args, err := p.Config.ParseArguments(tc.Arguments)
			if err != nil {
				badArgs[i] = fmt.Sprintf("Аргументы инструмента %q пришли невалидным JSON (%s), поэтому вызов НЕ выполнен. "+
					"Повтори вызов с корректным JSON: двойные кавычки, без висячих запятых, переводы строк внутри строк — как \n.",
					tc.Name, truncate(err.Error(), 160))
				Debugf("RUNNER: аргументы инструмента %q не разобрались: %v", tc.Name, err)
			}

			jobs[i] = toolCallJob{tc: tc, args: args}
			if len(reply.ToolCalls) > 1 && p.Config.ParallelEnabled && p.Config.IsParallelSafe(tc.Name) {
				parallel[i] = true
				nParallel++
			}
		}

		outcomes := make([]toolOutcome, len(jobs))

		if nParallel > 0 {
			limit := p.Config.ParallelMax
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
						rep.OnToolStart(j.tc.Name, truncate(j.tc.Arguments, 2000))
					}
					result, err := agent.CallFunction(j.tc.Name, j.args)
					outcomes[i] = toolOutcome{result: result, err: err}
					if rep != nil {
						rep.OnToolResult(j.tc.Name, truncate(string(result), 8000), err == nil && !ToolResultFailed(j.tc.Name, result))
					}
				}(i)
			}
			wg.Wait()

			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("контекст отменён во время выполнения инструментов: %w", err)
			}
		}

		var loopCalls []LoopRoundCall
		roundProgress := false
		for i, j := range jobs {
			var (
				result  []byte
				err     error
				blocked bool
			)
			sig := CallSignature(j.tc.Name, j.args)
			switch {
			case badArgs[i] != "":
				result = ToolErrorResult(badArgs[i])
			case parallel[i]:
				result, err = outcomes[i].result, outcomes[i].err
			default:
				if reason, isBlocked := loop.BlockedVerifyCall(j.tc.Name, j.args); isBlocked {
					blocked = true
					result = ToolErrorResult(reason)
					Debugf("RUNNER: раунд %d: вызов %q заблокирован verify guard: %s", round+1, j.tc.Name, reason)
					if rep != nil {
						rep.OnToolResult(j.tc.Name, truncate(string(result), 8000), false)
					}
					break
				}
				if rep != nil {
					rep.OnToolStart(j.tc.Name, truncate(j.tc.Arguments, 2000))
				}
				result, err = agent.CallFunction(j.tc.Name, j.args)
				if rep != nil {
					rep.OnToolResult(j.tc.Name, truncate(string(result), 8000), !ToolResultFailed(j.tc.Name, result))
				}
			}

			if err != nil {
				Debugf("RUNNER: инструмент %q вернул ошибку: %v", j.tc.Name, err)
				return nil, fmt.Errorf("выполнение инструмента %s: %w", j.tc.Name, err)
			}

			result = []byte(domain.SanitizeToolOutput(string(result)))

			callCounts[sig]++

			Debugf("RUNNER: результат инструмента %q: %s", j.tc.Name, truncate(string(result), 500))
			failed := ToolResultFailed(j.tc.Name, result)
			state, stateSample, stateLabel := LoopStateFromCall(j.tc.Name, j.args, result)
			vcmd := VerifyCommandOf(j.tc.Name, j.args)
			loopCalls = append(loopCalls, LoopRoundCall{
				Name:        j.tc.Name,
				Sig:         sig,
				Failed:      failed,
				Result:      truncate(string(result), LoopDigestResult),
				Paths:       LoopCallPaths(j.args),
				State:       state,
				StateSample: stateSample,
				StateLabel:  stateLabel,
				Verify:      vcmd != "" && LoopIsVerificationCommand(vcmd),
				VerifyCmd:   vcmd,
				Blocked:     blocked,
			})
			if !failed {
				roundProgress = true
			}
			if failed {
				failCounts[j.tc.Name]++
			}
			for gi, grp := range requiredGroups {
				if requiredDone[gi] {
					continue
				}
				if slices.Contains(grp, j.tc.Name) && !failed {
					requiredDone[gi] = true
					Debugf("RUNNER: обязательный инструмент %q выполнен успешно", j.tc.Name)
				}
			}
			messages = append(messages, domain.Message{
				Role:       "tool",
				ToolName:   j.tc.Name,
				ToolCallID: j.tc.ID,
				Content:    string(result),
			})

			if loop.BannedSignature(sig, j.tc.Name) && loop.RegisterStrike() {
				reason := fmt.Sprintf("после предупреждения о петле модель повторила запрещённый вызов %s (%d-й раз)", SignatureLabel(sig), loop.Strikes())
				Debugf("RUNNER: раунд %d: %s — цикл прерван (Looped)", round+1, reason)
				if rep != nil {
					rep.OnMessage("user", "Цикл агента прерван: "+reason, false)
				}
				return &domain.AgentResponse{
					Content:    content,
					ToolCalls:  allToolCalls,
					Looped:     true,
					LoopReason: reason,
					Rounds:     round + 1,
					Messages:   append([]domain.Message{}, messages...),
				}, nil
			}
		}

		autofixOn := p.Config.AutoFixEnabled
		reindexOn := p.Config.ReindexEnabled
		wipOn := p.Config.WIPEnabled
		var roundTouched []string
		var roundSHA string
		var roundCommitted bool
		if af, ok := agent.(AutoFixer); ok && (autofixOn || reindexOn || wipOn) {
			roundTouched = af.TakeTouched()
			touched := roundTouched

			if wipOn && len(touched) > 0 {
				if wc, ok := agent.(WIPCommitter); ok {
					if rep != nil {
						rep.OnToolStart("WipCommit", "промежуточный коммит правок раунда")
					}
					sha, committed, cerr := wc.CommitRoundTouched(touched, round+1)
					roundSHA, roundCommitted = sha, committed
					switch {
					case cerr != nil:
						if rep != nil {
							rep.OnToolResult("WipCommit", truncate("коммит не выполнен: "+cerr.Error(), 2000), false)
						}
						Debugf("RUNNER: раунд %d: промежуточный коммит: %v", round+1, cerr)
					case committed:
						if rep != nil {
							rep.OnToolResult("WipCommit", truncate(fmt.Sprintf("зафиксировано раундом %d: %s", round+1, sha), 2000), true)
						}
						Debugf("RUNNER: раунд %d: промежуточный коммит %s (%d файлов)", round+1, sha, len(touched))
					default:
						if rep != nil {
							rep.OnToolResult("WipCommit", "дерево чистое, коммит не создавался", true)
						}
					}
				}
			}

			if reindexOn && len(touched) > 0 {
				if ri, ok := agent.(Reindexer); ok {
					if rep != nil {
						rep.OnToolStart("RAGReindex", "переиндексация файлов, затронутых раундом")
					}
					n, rerr := ri.ReindexTouched(touched)
					if rep != nil {
						rep.OnToolResult("RAGReindex", truncate(fmt.Sprintf("переиндексировано файлов: %d", n), 8000), rerr == nil)
					}
					if rerr != nil {
						Debugf("RUNNER: раунд %d: переиндексация RAG: %v", round+1, rerr)
					}
				}
			}

			if autofixOn {
				if rep != nil {
					rep.OnToolStart("LspAutoFix", "диагностика файлов, затронутых раундом")
				}
				diags, hadMutation := af.LspCheckFiles(touched)
				fresh := FilterNewDiags(diags, sentDiags)
				if rep != nil {
					rep.OnToolResult("LspAutoFix", truncate(strings.Join(diags, "\n"), 8000), !hadMutation || len(diags) == 0)
				}
				switch {
				case !hadMutation:
				case len(diags) == 0:
					autoFixUsed = 0
					sentDiags = make(map[string]bool)
				case len(fresh) == 0:
					Debugf("RUNNER: раунд %d: авто-лечение: все %d диагностик уже отправлены, подсказка не нужна", round+1, len(diags))
				case autoFixUsed >= p.Config.AutoFixMaxRounds:
					Debugf("RUNNER: раунд %d: авто-лечение: лимит итераций (%d) исчерпан, подсказки прекращены", round+1, autoFixUsed)
				default:
					autoFixUsed++
					for _, d := range fresh {
						sentDiags[d] = true
					}
					Debugf("RUNNER: раунд %d: авто-лечение: подмешиваю подсказку с %d диагностиками (%d/%d)", round+1, len(fresh), autoFixUsed, p.Config.AutoFixMaxRounds)
					messages = append(messages, domain.Message{Role: "user", Content: AutoFixMessage(fresh, autoFixUsed, p.Config.AutoFixMaxRounds)})
				}
			}
		}

		if rs, ok := agent.(RoundStateReporter); ok && p.Config.BuildRoundState != nil {
			rs.ReportRoundState(p.Config.BuildRoundState(round+1, loopCalls, roundTouched, roundSHA, roundCommitted))
		}

		if appLogFeedOn {
			if rl, ok := agent.(RuntimeLogger); ok {
				source, rawLines := rl.TakeAppLogTail()
				fresh := FilterNewDiags(TrimAppLogLines(rawLines), sentAppLogs)
				switch {
				case len(rawLines) == 0:
				case len(fresh) == 0:
					Debugf("RUNNER: раунд %d: логи рантайма уже отправлены (%d строк), повтор пропущен", round+1, len(rawLines))
				case appLogFeedUsed >= p.Config.AppLogMaxFeedRounds:
					Debugf("RUNNER: раунд %d: лимит подмешиваний логов рантайма (%d) исчерпан", round+1, appLogFeedUsed)
				default:
					appLogFeedUsed++
					for _, l := range fresh {
						sentAppLogs[l] = true
					}
					Debugf("RUNNER: раунд %d: подмешиваю %d строк логов рантайма (%d/%d)", round+1, len(fresh), appLogFeedUsed, p.Config.AppLogMaxFeedRounds)
					messages = append(messages, domain.Message{Role: "user", Content: AppLogFeedMessage(source, fresh, appLogFeedUsed, p.Config.AppLogMaxFeedRounds)})
				}
			}
		}

		if roundProgress {
			loopProgressIdx = len(messages)
		}

		loop.ExpireBlocks()

		if reason := loop.ObserveRound(loopCalls); reason != "" {
			if !loop.CanBreak() {
				Debugf("RUNNER: раунд %d: петля подтверждена (%s), разрывы исчерпаны (%d) — цикл прерван (Looped)", round+1, reason, loop.Breaks())
				if rep != nil {
					rep.OnMessage("user", "Цикл агента прерван: "+reason, false)
				}
				return &domain.AgentResponse{
					Content:    content,
					ToolCalls:  allToolCalls,
					Looped:     true,
					LoopReason: reason,
					Rounds:     round + 1,
					Messages:   append([]domain.Message{}, messages...),
				}, nil
			}
			digest, banned := loop.BreakLoop(reason, pendingRequired())
			Debugf("RUNNER: раунд %d: детектировано зацикливание (%s) — разрыв петли %d/%d, под запретом %d вызов(ов)",
				round+1, reason, loop.Breaks(), loop.MaxBreaks(), banned)
			messages = LoopCollapsedMessages(messages, loopProgressIdx, loopPrefix)
			digest += LoopRestartMessage(loopTask, loop.TouchedFiles(), pendingRequired())
			messages = append(messages, domain.Message{Role: "user", Content: digest})
			if rep != nil {
				rep.OnMessage("user", digest, false)
			}
			loopProgressIdx = LoopPrefixLen(messages)
			callCounts = make(map[string]int)
			failCounts = make(map[string]int)
			loopNudged = make(map[string]bool)
			loopFailNudged = make(map[string]bool)
			continue
		}

		for sig, n := range callCounts {
			if n >= MaxRepeatedToolCalls && !loopNudged[sig] {
				loopNudged[sig] = true
				toolName := sig
				if i := strings.Index(toolName, " "); i >= 0 {
					toolName = toolName[:i]
				}
				Debugf("RUNNER: раунд %d: детектировал повторяющийся вызов %q (%d раз), подсказываю завершить цикл", round+1, toolName, n)
				messages = append(messages, domain.Message{Role: "user", Content: LoopMessage(toolName, n)})
				break
			}
		}

		if len(failCounts) > 0 {
			for name, n := range failCounts {
				if n >= MaxRepeatedToolFails && !loopFailNudged[name] {
					loopFailNudged[name] = true
					Debugf("RUNNER: раунд %d: инструмент %q вернул ошибки подряд (%d раз), подсказываю не перебирать их", round+1, name, n)
					messages = append(messages, domain.Message{Role: "user", Content: ToolFailMessage(name, n, pendingRequired())})
					break
				}
			}
		}

		if pending := pendingRequired(); pending != "" && progressNudges < MaxRequiredProgressNudges {
			done := round - startRound + 1
			if done >= RequiredProgressAfter && (done-RequiredProgressAfter)%RequiredProgressEvery == 0 {
				progressNudges++
				Debugf("RUNNER: раунд %d: обязательное действие %q не выполнено (%d раундов), подсказываю перейти к публикации (%d/%d)",
					round+1, pending, done, progressNudges, MaxRequiredProgressNudges)
				messages = append(messages, domain.Message{
					Role:    "user",
					Content: ProgressNudgeMessage(pending, round+1, startRound+mx, progressNudges),
				})
			}
		}
	}

	Debugf("RUNNER: достигнут лимит раундов (%d), возвращаю частичный результат", mx)
	return &domain.AgentResponse{
		Content:   content,
		ToolCalls: allToolCalls,
		Truncated: true,
		Rounds:    startRound + mx,
		Messages:  append([]domain.Message{}, messages...),
	}, nil
}

// toolCallJob — подготовленный вызов инструмента раунда: аргументы уже
// разобраны из JSON строки, к вызову готов.
type toolCallJob struct {
	tc   domain.ToolCall
	args map[string]any
}

// toolOutcome — результат выполнения одного вызова инструмента.
type toolOutcome struct {
	result []byte
	err    error
}
