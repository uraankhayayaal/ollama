package runner

import (
	"ai/agents"
	"ai/tools"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// buildPair — маленькая пара assistant(tool_calls)+tool для построения истории.
func buildPair(id, content string) []Message {
	return []Message{
		{Role: "assistant", ToolCalls: []tools.ToolCall{{ID: id, Name: "EditFiles", Arguments: `{"path":"src/` + id + `.go"}`}}},
		{Role: "tool", ToolName: "EditFiles", ToolCallID: id, Content: content},
	}
}

// Ф-7: вытеснение выброшенных юнитов вызывается с реальным контентом; флаг
// выключен — функции не вызываются (degrade, но без затрат).
func TestCompressContextEvict(t *testing.T) {
	var got []EvictionItem
	var gotProject string
	msgs := []Message{
		msg("system", "sys"),
		msg("user", "задача"),
	}
	msgs = append(msgs, buildPair("1", strings.Repeat("x", 500))...)
	msgs = append(msgs, buildPair("2", strings.Repeat("y", 500))...)
	msgs = append(msgs, msg("assistant", "итог"))

	out, rep := CompressContext(context.Background(), msgs, CompressOptions{
		Budget:  100,
		Project: "proj-x",
		Evict:   true,
		EvictFn: func(ctx context.Context, project string, items []EvictionItem) error {
			gotProject = project
			got = items
			return nil
		},
	})
	_ = out
	if rep.Dropped == 0 || len(rep.EvictionItems) == 0 {
		t.Fatalf("ожидали выброс и элементы вытеснения, dropped=%d items=%d", rep.Dropped, len(rep.EvictionItems))
	}
	if gotProject != "proj-x" {
		t.Fatalf("проект не передан: %q", gotProject)
	}
	if len(got) != len(rep.EvictionItems) {
		t.Fatalf("sink получил %d элементов, ожидали %d", len(got), len(rep.EvictionItems))
	}
	// Каждый элемент несёт либо пометку инструмента, либо содержимое.
	for _, it := range got {
		if it.Tool == "" && strings.TrimSpace(it.Text) == "" {
			t.Fatalf("пустой элемент вытеснения: %+v", it)
		}
	}

	// Флаг выключен — функция не вызывается.
	called := false
	CompressContext(context.Background(), msgs, CompressOptions{
		Budget: 100,
		Evict:  false,
		EvictFn: func(context.Context, string, []EvictionItem) error {
			called = true
			return nil
		},
	})
	if called {
		t.Fatal("Evict=false не должен вызывать EvictFn")
	}
}

// Ф-7: ошибка вытеснения не роняет сжатие, уходит в report.Errors.
func TestCompressContextEvictErrorDegrades(t *testing.T) {
	msgs := []Message{msg("system", "sys"), msg("user", "задача")}
	msgs = append(msgs, buildPair("1", strings.Repeat("x", 500))...)
	msgs = append(msgs, msg("assistant", "итог"))

	_, rep := CompressContext(context.Background(), msgs, CompressOptions{
		Budget: 10,
		Evict:  true,
		EvictFn: func(context.Context, string, []EvictionItem) error {
			return errors.New("qdrant недоступен")
		},
	})
	if len(rep.Errors) == 0 {
		t.Fatal("ошибка вытеснения должна попасть в report.Errors")
	}
}

// Ф-8: retrieval-guided ранжирование сохраняет релевантные юниты, а не только
// по позиции; ошибка ранжировщика фолбэчит на позиционный выброс (все середины).
func TestCompressContextRankKeepsRelevant(t *testing.T) {
	// Четыре пары разного веса: релевантная (0) лёгкая, остальные тяжёлые.
	// Бюджет оставляет остаток ровно под лёгкую — выживает только она.
	var msgs []Message
	msgs = append(msgs, msg("system", "sys"), msg("user", "задача"))
	headW := estimateLen(msgs)
	for i := 0; i < 4; i++ {
		id := fmt.Sprintf("%d", i)
		content := strings.Repeat("h", 1000)
		if i == 0 {
			content = strings.Repeat("l", 100)
		}
		pair := []Message{
			{Role: "assistant", Content: fmt.Sprintf("%d-step", i), ToolCalls: []tools.ToolCall{{ID: id, Name: "EditFiles", Arguments: fmt.Sprintf(`{"path":"src/%s.go"}`, id)}}},
			{Role: "tool", ToolName: "EditFiles", ToolCallID: id, Content: content},
		}
		msgs = append(msgs, pair...)
	}
	msgs = append(msgs, msg("assistant", "итог"))

	heavyW := estimateLen(msgs[4:6]) // пара №1 (тяжёлая)
	lightW := estimateLen(msgs[2:4]) // пара №0 (лёгкая)
	if heavyW <= lightW {
		t.Fatalf("ожидали тяжёлую пару тяжелее лёгкой: heavy=%d light=%d", heavyW, lightW)
	}
	budget := headW + len("итог") + heavyW + lightW + 10

	var rankCalled []string
	out, rep := CompressContext(context.Background(), msgs, CompressOptions{
		Budget: budget,
		Rank:   true,
		RankFn: func(ctx context.Context, query string, texts []string) ([]float32, error) {
			rankCalled = append(rankCalled, query)
			s := make([]float32, len(texts))
			for i, txt := range texts {
				if strings.Contains(txt, "0-step") {
					s[i] = 10
				} else {
					s[i] = 0
				}
			}
			return s, nil
		},
	})
	if len(rankCalled) != 1 {
		t.Fatalf("ранжировщик должен быть вызван один раз, было %d", len(rankCalled))
	}
	if rep.Evicted == 0 {
		t.Fatalf("ожидали сжатие, Evicted=%d", rep.Evicted)
	}
	found := map[string]bool{}
	for _, m := range out {
		if m.Role == "tool" {
			found[m.ToolCallID] = true
		}
	}
	if !found["0"] || !found["3"] {
		t.Fatalf("слоты из головы-релевантной (0) и хвоста (3) должны сохраниться, got %v out=%d", found, len(out))
	}
	if found["1"] || found["2"] {
		t.Fatalf("нерелевантные пары 1/2 не должны сохраняться, got %v", found)
	}
	if len(out) >= len(msgs) {
		t.Fatalf("история должна сжаться: %d -> %d", len(msgs), len(out))
	}
	// Хвост на месте.
	if out[len(out)-1].Content != "итог" {
		t.Fatalf("хвост должен сохраниться, got %q", out[len(out)-1].Content)
	}
}

// Ф-8: ошибка ранжировщика — позиционный выброс, с предупреждением в Errors.
func TestCompressContextRankErrorFallsBack(t *testing.T) {
	msgs := []Message{msg("system", "sys"), msg("user", "задача")}
	msgs = append(msgs, buildPair("1", strings.Repeat("x", 400))...)
	msgs = append(msgs, buildPair("2", strings.Repeat("y", 400))...)
	msgs = append(msgs, msg("assistant", "итог"))

	_, rep := CompressContext(context.Background(), msgs, CompressOptions{
		Budget: 50,
		Rank:   true,
		RankFn: func(context.Context, string, []string) ([]float32, error) {
			return nil, errors.New("embedder недоступен")
		},
	})
	if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0], "ранжирование") {
		t.Fatalf("ожидали warning о фолбэке, got %v", rep.Errors)
	}
	if rep.Evicted != 2 {
		t.Fatalf("фолбэк должен выбросить все середины, Evicted=%d", rep.Evicted)
	}
}

// Ф-9 + Ф-10 + Ф-6: оглавления файлов из выброшенного контента попадают в
// функции и в памятку; компакция даёт резюме; памятка дописывается в систему.
func TestCompressContextOutlineCompactNotice(t *testing.T) {
	var gotRels, gotText string
	msgs := []Message{
		msg("system", "sys"),
		msg("user", "задача"),
		Message{Role: "assistant", ToolCalls: []tools.ToolCall{{ID: "1", Name: "EditFiles", Arguments: `{}`}}},
		{Role: "tool", ToolName: "EditFiles", ToolCallID: "1", Content: "=== cmd/tool.go ===\nфрагмент правки\n=== internal/x.go ===\nещё правка"},
		Message{Role: "assistant", ToolCalls: []tools.ToolCall{{ID: "2", Name: "EditFiles", Arguments: `{}`}}},
		{Role: "tool", ToolName: "EditFiles", ToolCallID: "2", Content: strings.Repeat("z", 400)},
		msg("assistant", "итог"),
	}

	out, rep := CompressContext(context.Background(), msgs, CompressOptions{
		Budget: 10,
		Compact: true,
		CompactFn: func(ctx context.Context, text string) (string, error) {
			gotText = text
			return "резюме: правка cmd/tool.go и internal/x.go", nil
		},
		Outline: true,
		OutlineFn: func(ctx context.Context, project string, rels []string) (string, error) {
			gotRels = strings.Join(rels, ",")
			return "cmd/tool.go\nfunction main : 5", nil
		},
		Notice: true,
	})
	if rep.Summary == "" {
		t.Fatal("компакция должна дать резюме")
	}
	if !strings.Contains(gotText, "tool EditFiles") {
		t.Fatalf("компактор должен получить текст вытесненного, got %.60q", gotText)
	}
	if !strings.Contains(gotRels, "cmd/tool.go") || !strings.Contains(gotRels, "internal/x.go") {
		t.Fatalf("оглавления должны получить пути из контента, got %q", gotRels)
	}
	if rep.Outlines == "" {
		t.Fatal("оглавления должны попасть в отчёт")
	}
	// Памятка: в отчёте и в последнем system-сообщении головы.
	if rep.Notice == "" {
		t.Fatal("памятка должна быть составлена")
	}
	sysLast := out[0].Content
	if !strings.Contains(sysLast, "сжата") || !strings.Contains(sysLast, "резюме") || !strings.Contains(sysLast, "cmd/tool.go") {
		t.Fatalf("памятка должна быть в системе головы, got %.200q", sysLast)
	}
}

// Ф-6: без system-сообщений памятка вставляется первым сообщением.
func TestCompressContextNoticeNoSystem(t *testing.T) {
	msgs := []Message{msg("user", "задача")}
	msgs = append(msgs, buildPair("1", strings.Repeat("x", 500))...)
	msgs = append(msgs, msg("assistant", "итог"))

	out, rep := CompressContext(context.Background(), msgs, CompressOptions{Budget: 10, Notice: true})
	if rep.Notice == "" {
		t.Fatal("памятка должна быть")
	}
	if out[0].Role != "system" || !strings.Contains(out[0].Content, "сжата") {
		t.Fatalf("без системы памятка должна быть первым сообщением, got %+v", out[0])
	}
}

// Ф-11 + клиент: ужесточение бюджета по фактическому usage; контекстный клиент
// собирает опции из env (флаги) и сохраняет функции.
func TestCompressClientUsageAwareBudget(t *testing.T) {
	// Токен-лимит задан (Ф-11), символьного бюджета нет — сжатия не будет, пока
	// фактический вход не превысит лимит.
	t.Setenv("CODEGEN_HISTORY_TOKENS", "50")
	t.Setenv("CODEGEN_HISTORY_BUDGET", "")

	msgs := []Message{msg("system", "sys"), msg("user", "задача")}
	msgs = append(msgs, buildPair("1", strings.Repeat("x", 500))...)
	msgs = append(msgs, buildPair("2", strings.Repeat("y", 500))...)
	msgs = append(msgs, msg("assistant", "итог"))

	// Бюджет не задан и usage в норме — история не тронута.
	compacted, _ := compressHistoryForRound(context.Background(), nil, msgs, 10)
	if len(compacted) != len(msgs) {
		t.Fatalf("в пределах токен-лимита сжатие не нужно: %d -> %d", len(msgs), len(compacted))
	}

	// Фактический usage превысил лимит — пересчитанный бюджет (лимит*4 символов)
	// применяется и история сжимается.
	compacted, rep := compressHistoryForRound(context.Background(), nil, msgs, 2000)
	if len(compacted) == len(msgs) || rep.Dropped == 0 {
		t.Fatalf("токен-лимит должен включить сжатие, dropped=%d", rep.Dropped)
	}
}

// Опции клиента: status-флаги подхватываются из env, остальное — из структуры.
func TestCompressionClientOptions(t *testing.T) {
	t.Setenv("CODEGEN_HISTORY_NOTICE", "1")
	t.Setenv("CODEGEN_HISTORY_EVICT", "0")

	c := &CompressionClient{Project: "p"}
	if c.options().Notice != true {
		t.Fatal("NOTICE=1 должен включить памятку")
	}
	if c.options().Evict {
		t.Fatal("EVICT=0 не должен включать вытеснение")
	}

	c2 := &CompressionClient{Project: "p2", Rank: nil}
	opts2 := c2.options()
	if opts2.Rank {
		t.Fatal("без клалки rank не должен включаться")
	}

	ctx := WithCompressionClient(context.Background(), c)
	if got := CompressionClientFromContext(ctx); got != c {
		t.Fatal("клиент должен пройти через контекст")
	}
	if CompressionClientFromContext(context.Background()) != nil {
		t.Fatal("без клиента контекст должен отдавать nil")
	}
}

// extractFiles — детерминированные пути из маркеров и JSON-полей, мусор выкидывается.
func TestExtractFiles(t *testing.T) {
	in := []string{
		"=== server/main.go ===\nкакая-то правка",
		`говорит {"file":"internal/auth/token.go"} и всё`,
		"=== @episode/foo ===\nшум",
		"=== .git/config ===\n=== node_modules/lodash/x.js ===",
	}
	got := extractFiles(in)
	if len(got) != 2 {
		t.Fatalf("ожидали 2 пути, got %v", got)
	}
	if got[0] != "internal/auth/token.go" || got[1] != "server/main.go" {
		t.Fatalf("extractFiles: got %v", got)
	}
}

// limitsProvider — fake-провайдер с лимитами модели для тестов авто-сжатия
// по окну провайдера.
type limitsProvider struct {
	limits ModelLimits
}

func (p *limitsProvider) ModelLimits() ModelLimits { return p.limits }
func (p *limitsProvider) ChatOnce(_ context.Context, _ agents.Agent, _ []Message) (*ModelReply, error) {
	return nil, nil
}

// providerInputCap — лимит входа: окно минус резерв под вывод (ответ+thinking);
// неизвестное окно и провайдер без лимитов — 0.
func TestProviderInputCap(t *testing.T) {
	cases := []struct {
		name string
		ml   ModelLimits
		want int
	}{
		{"окно с выводом и thinking", ModelLimits{InputTokens: 32768, OutputTokens: 16384, ThinkTokens: 4096}, 12288},
		{"окно только с выводом", ModelLimits{InputTokens: 32768, OutputTokens: 16384}, 16384},
		{"окно без вывода — четверть резерв", ModelLimits{InputTokens: 32768}, 24576},
		{"окно без лимитов", ModelLimits{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerInputCap(&limitsProvider{limits: tc.ml}); got != tc.want {
				t.Fatalf("providerInputCap = %d, want %d", got, tc.want)
			}
		})
	}
	if providerInputCap(nil) != 0 {
		t.Fatal("nil-провайдер не должен давать лимит")
	}
}

// Авто-сжатие по окну провайдера без env-бюджета (CODEGEN_HISTORY_* выключены):
// разросшаяся история, превышающая окно, сжимается; в пределах окна — нет.
func TestCompressHistoryProviderWindowDefault(t *testing.T) {
	t.Setenv("CODEGEN_HISTORY_BUDGET", "")
	t.Setenv("CODEGEN_HISTORY_TOKENS", "")
	provider := &limitsProvider{limits: ModelLimits{InputTokens: 32768, OutputTokens: 16384}}

	// История в пределах окна (вход на ~50 токенов) — не тронута и при раздутом
	// usage прошлого раунда: сжимать нечего, история уже в бюджете.
	small := []Message{msg("system", "sys"), msg("user", "задача"), msg("assistant", "краткий ответ")}
	for i := 1; i <= 3; i++ {
		small = append(small, buildPair(fmt.Sprint(i), "короткий результат")...)
	}
	compacted, _ := compressHistoryForRound(context.Background(), provider, small, 20000)
	if len(compacted) != len(small) {
		t.Fatalf("малая история не должна сжиматься: %d -> %d", len(small), len(compacted))
	}

	// Разросшаяся история (оценка входа > кап = окно − вывод) — старые
	// середины выбрасываются, голова и хвост сохраняются. Триггер идёт и по
	// упреждающей оценке текущего раунда (lastInputTokens=0), и по реальному
	// usage прошлого раунда — оба включают сжатие по умолчанию.
	task := strings.Repeat("Д", 2000)
	var big []Message
	big = append(big, msg("system", "sys"), msg("user", "задача"))
	for i := 0; i < 40; i++ {
		big = append(big, buildPair(fmt.Sprint(i), task)...)
	}
	big = append(big, msg("assistant", "финал"))

	for _, last := range []int{0, 20000} {
		compacted, rep := compressHistoryForRound(context.Background(), provider, big, last)
		if len(compacted) == len(big) || rep.Dropped == 0 {
			t.Fatalf("вход выше окна (usage=%d) должен включить сжатие по умолчанию", last)
		}
		if compacted[0].Content != "sys" || compacted[len(compacted)-1].Content != "финал" {
			t.Fatalf("голова и хвост должны сохраниться, got first=%q last=%q",
				compacted[0].Content, compacted[len(compacted)-1].Content)
		}
	}
}

// Защита: тесты не зависят от переменных окружения (чистка известных флагов).
func TestMain(m *testing.M) {
	for _, k := range []string{
		"CODEGEN_HISTORY_BUDGET", "CODEGEN_HISTORY_TOKENS",
		"CODEGEN_HISTORY_NOTICE", "CODEGEN_HISTORY_EVICT",
		"CODEGEN_HISTORY_RANK", "CODEGEN_HISTORY_OUTLINE", "CODEGEN_HISTORY_COMPACT",
	} {
		_ = os.Setenv(k, "")
	}
	os.Exit(m.Run())
}