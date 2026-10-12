package runner

import (
	"ai/application/usecases"
	"ai/tools"
	"fmt"
	"strings"
	"testing"
)

func containsInMessages(messages []Message, needle string) bool {
	for _, m := range messages {
		if strings.Contains(m.Content, needle) {
			return true
		}
	}
	return false
}

// Нормализация вывода проверки убирает шум, из-за которого одинаковая ошибка
// выглядит новой: пути временных каталогов, метки времени, тайминги, номера
// строк, счётчики и цвета терминала.
func TestNormalizeVerifyOutputStripsNoise(t *testing.T) {
	first := "\x1b[31mFAIL\x1b[0m src/Login.test.tsx\n" +
		"  ✕ проверка логина (12 ms)\n" +
		"    /home/user/app/temp/.wt-task-7/src/Login.tsx:42:15\n" +
		"    Tests: 1 failed, 2 passed, 3 total\n" +
		"    12:04:31 - ожидание waitFor\n"
	// Те же ошибки со сдвинутой строкой, другим каталогом и временем — для
	// детектора это ОДНО И ТО ЖЕ состояние.
	second := "FAIL src/Login.test.tsx\n" +
		"  ✕ проверка логина (980 ms)\n" +
		"    /tmp/other/worktree/src/Login.tsx:57:9\n" +
		"    Tests: 1 failed, 2 passed, 3 total\n" +
		"    23:59:02 - ожидание waitFor\n"

	if usecases.NormalizeVerifyOutput(first) != usecases.NormalizeVerifyOutput(second) {
		t.Fatalf("шум не нормализован:\n%s\n---\n%s", usecases.NormalizeVerifyOutput(first), usecases.NormalizeVerifyOutput(second))
	}

	// Номера строк схлопнуты, но имя файла и суть ошибки остались.
	norm := usecases.NormalizeVerifyOutput(first)
	for _, want := range []string{"/…/Login.tsx", "Login.test.tsx", "waitFor", "<стр>", "<длительность>", "<время>"} {
		if !strings.Contains(norm, want) {
			t.Fatalf("нормализованный вывод должен содержать %q, got:\n%s", want, norm)
		}
	}
	if strings.Contains(norm, "/home/user") || strings.Contains(norm, "12:04:31") {
		t.Fatalf("абсолютные пути и метки времени должны быть убраны, got:\n%s", norm)
	}
}

// Разные ошибки дают разные отпечатки состояния.
func TestNormalizeVerifyOutputKeepsRealDifference(t *testing.T) {
	a := usecases.NormalizeVerifyOutput("FAIL src/a.test.tsx:10:1\nExpected 'user' to be 'admin'")
	b := usecases.NormalizeVerifyOutput("FAIL src/b.test.tsx:10:1\nTypeError: cannot read property 'id' of undefined")
	if a == b {
		t.Fatalf("разные ошибки должны давать разные отпечатки:\n%s\n%s", a, b)
	}
}

// Отпечаток состояния строится только для проверочных команд и только по их
// ПАДЕНИЮ: успешная сборка состояния не меняет.
func TestLoopStateFromCall(t *testing.T) {
	runArgs := map[string]any{"command": "make frontend-test"}
	failed := []byte(`{"status":"error","exit_error":"exit status 1","stderr":"Tests: 1 failed"}`)

	state, sample, label := usecases.LoopStateFromCall("Run", runArgs, failed)
	if state == "" || sample == "" || label != "make frontend-test" {
		t.Fatalf("падение проверки должно давать отпечаток: state=%q sample=%q label=%q", state, sample, label)
	}
	if state != "make frontend-test" {
		t.Fatalf("отпечаток — нормализованная проверка, got %q", state)
	}

	if _, _, _ = usecases.LoopStateFromCall("Run", runArgs, []byte(`{"status":"success"}`)); false {
		t.Fatal("успешная проверка не должна считаться падением")
	}

	// Не проверочная команда отпечатка не даёт.
	if s, _, _ := usecases.LoopStateFromCall("Run", map[string]any{"command": "git status"}, failed); s != "" {
		t.Fatalf("git status не должен давать отпечаток состояния, got %q", s)
	}
	// Другие инструменты — тоже.
	if s, _, _ := usecases.LoopStateFromCall("WriteFiles", nil, failed); s != "" {
		t.Fatalf("WriteFiles не должен давать отпечаток состояния, got %q", s)
	}
}

// Сквозной случай из жалобы: каждый раунд агент делает формально новые вызовы
// (сдвинутые строки чтения, разные ключи, разная строка команды проверки), но
// проверка падает с тем же выводом. Сигнатуры уникальны — ловит только отпечаток
// состояния.
func TestGenerateStateHashBreaksLoopWithVaryingArgs(t *testing.T) {
	// Результат Run: одна и та же ошибка с разным временем и путём worktree.
	runResult := func() []byte {
		return []byte(`{"status":"error","exit_error":"exit status 1",` +
			`"stderr":"Tests: 1 failed, 2 passed\n/tmp/wt-42/src/Login.test.tsx:12:5\n12:04:31 async waitFor"}`)
	}
	agent := &fakeAgent{callResults: [][]byte{runResult()}}

	// Каждый раунд — формально новые вызовы: сдвинутое чтение и другая строка
	// команды проверки. Именно их сигнатурный детектор пропускает.
	reply := func(i int, cmd string) *ModelReply {
		return &ModelReply{
			FinishReason: "tool_calls",
			ToolCalls: []tools.ToolCall{
				{Name: "ReadFiles", Arguments: fmt.Sprintf(`{"paths":["src/Login.tsx"],"start":%d}`, i*10)},
				{Name: "Run", Arguments: fmt.Sprintf(`{"command":%q}`, cmd)},
			},
		}
	}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		reply(1, "make frontend-test 2>&1 | tail -20"),
		reply(2, "make frontend-test 2>&1 | grep Login"),
		reply(3, "make frontend-test 2>&1 | head -40"),
		reply(4, "make frontend-test 2>&1 | grep -B3 -A20 stderr"),
		// после разрыва — другой подход
		{Content: "починил иначе", FinishReason: "stop"},
	}}}

	resp := testGenerateAny(t, agent, provider)
	if resp.Looped {
		t.Fatalf("цикл не должен прерываться: %s", resp.LoopReason)
	}
	if resp.Content != "починил иначе" {
		t.Fatalf("ожидался финальный ответ, got %q", resp.Content)
	}

	// Предупреждение должно уйти в запрос модели ДО разрыва петли: показанное
	// после схлопывания истории предупреждение пропало бы вместе с ней.
	warnRound := -1
	for i, msgs := range provider.received {
		if containsInMessages(msgs, "ВНИМАНИЕ ХАРНЕСА") {
			warnRound = i
			break
		}
	}
	if warnRound < 0 {
		t.Fatal("предупреждение о неподвижной проверке не дошло до запроса модели")
	}
	breakRound := -1
	for i, msgs := range provider.received {
		if containsInMessages(msgs, "СТОП: ты попал в повторяющийся цикл") {
			breakRound = i
			break
		}
	}
	if breakRound < 0 {
		t.Fatal("разрыв петли не дошёл до запроса модели")
	}
	if warnRound >= breakRound {
		t.Fatalf("предупреждение (раунд %d) должно быть раньше разрыва (раунд %d)", warnRound, breakRound)
	}

	digest := findDigest(resp.Messages)
	if !strings.Contains(digest, "состояние проекта не меняется") {
		t.Fatalf("разрыв должен быть по неизменному состоянию, got:\n%s", digest)
	}
	if !containsInMessages(resp.Messages, "Харнес заблокировал повторный запуск проверки") {
		t.Fatal("бесполезный перезапуск падающей проверки должен блокироваться")
	}
}

// Дефект JSON от модели больше не роняет оркестрацию: вызов не выполняется, но
// запуск продолжается, а модель получает требование повторить с валидным JSON.
func TestGenerateSurvivesInvalidToolCallJSON(t *testing.T) {
	// Висячая запятая — типичный дефект локальных моделей: чинится на разборе.
	commated := tools.ToolCall{Name: "WriteFiles", Arguments: `{"files":[{"filename":"a.go","content":"x"}],}`}
	// JSON, утопленный в ```-блок с пояснением после объекта: тоже чинится.
	noisy := tools.ToolCall{Name: "ReadFiles", Arguments: "мусор ```json\n{\"paths\":[\"a.go\"]}\n"}
	// Обрезанный объект: не чинится — вызов не выполняется, но запуск живёт.
	truncated := tools.ToolCall{Name: "List", Arguments: `{"files":`}

	agent := &fakeAgent{}
	provider := &fakeChatProvider{replies: []*ModelReply{
		{ToolCalls: []tools.ToolCall{commated}, FinishReason: "tool_calls"},
		{ToolCalls: []tools.ToolCall{noisy}, FinishReason: "tool_calls"},
		{ToolCalls: []tools.ToolCall{truncated}, FinishReason: "tool_calls"},
		{Content: "готово", FinishReason: "stop"},
	}}

	resp := testGenerate(t, agent, provider)
	if resp.Looped {
		t.Fatalf("цикл не должен прерываться: %s", resp.LoopReason)
	}
	if resp.Content != "готово" {
		t.Fatalf("ожидался финальный ответ, got %q", resp.Content)
	}

	var sawInvalidHint bool
	for _, m := range resp.Messages {
		if strings.Contains(m.Content, "невалидным JSON") {
			sawInvalidHint = true
		}
	}
	if !sawInvalidHint {
		t.Fatalf("модель должна получить требование повторить вызов с валидным JSON: %#v", resp.Messages)
	}
}
