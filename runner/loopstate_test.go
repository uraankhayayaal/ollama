package runner

import (
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

	if normalizeVerifyOutput(first) != normalizeVerifyOutput(second) {
		t.Fatalf("шум не нормализован:\n%s\n---\n%s", normalizeVerifyOutput(first), normalizeVerifyOutput(second))
	}

	// Номера строк схлопнуты, но имя файла и суть ошибки остались.
	norm := normalizeVerifyOutput(first)
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
	a := normalizeVerifyOutput("FAIL src/a.test.tsx:10:1\nExpected 'user' to be 'admin'")
	b := normalizeVerifyOutput("FAIL src/b.test.tsx:10:1\nTypeError: cannot read property 'id' of undefined")
	if a == b {
		t.Fatalf("разные ошибки должны давать разные отпечатки:\n%s\n%s", a, b)
	}
}

// Отпечаток состояния строится только для проверочных команд и только по их
// ПАДЕНИЮ: успешная сборка состояния не меняет.
func TestLoopStateFromCall(t *testing.T) {
	runArgs := map[string]any{"command": "make frontend-test"}
	failed := []byte(`{"status":"error","exit_error":"exit status 1","stderr":"Tests: 1 failed"}`)

	state, sample, label := loopStateFromCall("Run", runArgs, failed)
	if state == "" || sample == "" || label != "make frontend-test" {
		t.Fatalf("падение проверки должно давать отпечаток: state=%q sample=%q label=%q", state, sample, label)
	}
	if state != "make frontend-test" {
		t.Fatalf("отпечаток — нормализованная проверка, got %q", state)
	}

	if _, _, _ = loopStateFromCall("Run", runArgs, []byte(`{"status":"success"}`)); false {
		t.Fatal("успешная проверка не должна считаться падением")
	}

	// Не проверочная команда отпечатка не даёт.
	if s, _, _ := loopStateFromCall("Run", map[string]any{"command": "git status"}, failed); s != "" {
		t.Fatalf("git status не должен давать отпечаток состояния, got %q", s)
	}
	// Другие инструменты — тоже.
	if s, _, _ := loopStateFromCall("WriteFiles", nil, failed); s != "" {
		t.Fatalf("WriteFiles не должен давать отпечаток состояния, got %q", s)
	}
}

// Ключевой случай жалобы: агент каждый раунд делает формально новый вызов
// (другие строки/ключи), но состояние проекта и вывод падения — те же. Детектор
// обязан увидеть петлю по состоянию, а не по сигнатуре.
func TestLoopDetectorDetectsUnchangedStateWithVaryingCalls(t *testing.T) {
	d := newLoopDetector()
	d.cfg.sigRepeats = 50 // сигнатуры «разные» — их не ловим
	d.cfg.failStreak = 50
	d.cfg.noWrite = 50

	var reason string
	for i := 0; i < 3; i++ {
		calls := []loopRoundCall{
			{name: "ReadFiles", sig: "ReadFiles {\"paths\":[\"a" + string(rune('a'+i)) + ".go\"]}"},
			{name: "Run", sig: "Run {\"command\":\"make frontend-test " + string(rune('a'+i)) + "\"}",
				failed: true, state: "deadbeef", stateSample: "Tests: 1 failed", stateLabel: "make frontend-test"},
		}
		reason = d.observeRound(calls)
	}

	if reason == "" {
		t.Fatal("одинаковое падение проверки при неизменном проекте должно ловиться")
	}
	if !strings.Contains(reason, "состояние проекта не меняется") {
		t.Fatalf("причина должна называть неизменное состояние, got %q", reason)
	}
}

// Правка проекта сбрасывает отсчёт состояния: падающая проверка после реального
// изменения — это нормальная работа, а не петля.
func TestLoopDetectorStateResetsOnChange(t *testing.T) {
	d := newLoopDetector()
	d.cfg.stateRepeats = 3

	failing := func() []loopRoundCall {
		return []loopRoundCall{{name: "Run", sig: "Run {\"command\":\"make test\"}", failed: true, state: "s1"}}
	}
	d.observeRound(failing())
	d.observeRound(failing())
	// Правка прошла успешно — состояние другое.
	d.observeRound([]loopRoundCall{{name: "WriteFiles", sig: "WriteFiles {\"files\":1}", failed: false}})
	d.observeRound(failing())

	if d.stateRuns["s1"] != 1 {
		t.Fatalf("после успешной правки отсчёт состояния должен начаться заново, got %d", d.stateRuns["s1"])
	}
}

// Verify guard (сеть на случай правок при том же падении): N одинаковых
// падений проверки подряд включают вмешательство и временную блокировку
// повторного запуска.
func TestLoopVerifyGuardInjectsAndBlocks(t *testing.T) {
	d := newLoopDetector()
	d.cfg.verifyFails = 5
	d.cfg.stateWarn = 0 // здесь проверяем именно сеть поверх правок

	call := func() []loopRoundCall {
		return []loopRoundCall{{
			name: "Run", sig: "Run {\"command\":\"make frontend-test\"}", failed: true,
			state: "make frontend-test", stateSample: "Tests: 1 failed, Login.test.tsx:12", stateLabel: "make frontend-test",
		}}
	}

	for i := 1; i < 5; i++ {
		d.observeRound(call())
		if inj := d.takeInjection(); inj != "" {
			t.Fatalf("вмешательство слишком рано: раунд %d", i)
		}
	}
	d.observeRound(call())

	injection := d.takeInjection()
	if injection == "" {
		t.Fatal("после 5 одинаковых падений должно быть принудительное вмешательство")
	}
	for _, want := range []string{"make frontend-test", "Login.test.tsx", "waitFor", "не запускай её снова"} {
		if !strings.Contains(strings.ToLower(injection), strings.ToLower(want)) {
			t.Fatalf("вмешательство должно содержать %q, got:\n%s", want, injection)
		}
	}

	// Повторный запуск той же проверки заблокирован, чужой вызов — нет.
	if reason, blocked := d.blockedVerifyCall("Run", map[string]any{"command": "make frontend-test"}); !blocked {
		t.Fatal("повторный запуск падавшей проверки должен быть заблокирован")
	} else if !strings.Contains(reason, "заблокировал") {
		t.Fatalf("в отказе должно быть объяснение, got %q", reason)
	}
	if _, blocked := d.blockedVerifyCall("Run", map[string]any{"command": "git status"}); blocked {
		t.Fatal("блокировка не должна затрагивать другие команды")
	}

	// Блокировка живёт ровно LOOP_VERIFY_BLOCK_ROUNDS раундов.
	for i := 0; i < d.cfg.verifyBlockRounds; i++ {
		d.expireBlocks()
	}
	if _, blocked := d.blockedVerifyCall("Run", map[string]any{"command": "make frontend-test"}); blocked {
		t.Fatal("после истечения срока блокировка должна сняться")
	}
}

// Вмешательство не является разрывом петли: бюджет разрывов не тратится, но
// повториться может.
func TestLoopVerifyGuardResetsAfterInjection(t *testing.T) {
	d := newLoopDetector()
	d.cfg.verifyFails = 2
	d.cfg.verifyBlockRounds = 0
	d.cfg.stateWarn = 0

	call := func() []loopRoundCall {
		return []loopRoundCall{{name: "Run", sig: "Run {\"command\":\"make test\"}", failed: true, state: "h", stateLabel: "make test"}}
	}
	for i := 0; i < 6; i++ {
		d.observeRound(call())
	}

	if d.breaks != 0 {
		t.Fatalf("verify guard не должен тратить разрывы петли, got %d", d.breaks)
	}
	if inj := d.takeInjection(); inj == "" {
		t.Fatal("вмешательства должны повторяться")
	}
}

// Идентичность проверки не зависит от того, как агент подкручивает фильтр
// вывода. Это ровно то, чем маскировалась живая петля FEL-02: одна и та же
// падающая проверка, но `| grep -A5`, `| grep -B3 -A20`, `| grep -i console` —
// и команды, и ВЫВОД каждый раунд разные.
func TestVerifyIdentityIgnoresPipelineAndFlags(t *testing.T) {
	same := []string{
		`cd frontend && npm test -- --run src/pages/Login.test.tsx 2>&1 | grep -B5 -A30 "FAIL\|Error"`,
		`cd frontend && npm test -- --run src/pages/Login.test.tsx 2>&1 | grep -A5 "stderr"`,
		`cd /home/user/app/temp/.wt-task-42/frontend && npm test -- --run src/pages/Login.test.tsx 2>&1 | grep -i "console\|warn" | tail -20`,
		`cd frontend && npm test -- --run src/pages/Login.test.tsx`,
	}
	want := verifyIdentity(same[0])
	if want != "cd … && npm test src/pages/Login.test.tsx" {
		t.Fatalf("неожиданная идентичность: %q", want)
	}
	for _, cmd := range same[1:] {
		if got := verifyIdentity(cmd); got != want {
			t.Fatalf("фильтр вывода не должен менять идентичность проверки:\n%q\n%q != %q", cmd, got, want)
		}
	}

	// Разная проверка — разная идентичность: соседние тесты не должны слиться.
	other := verifyIdentity(`cd frontend && npm test -- --run src/pages/Register.test.tsx 2>&1 | grep FAIL`)
	if other == want {
		t.Fatal("разные тесты должны давать разные отпечатки состояния")
	}
}

// Предупреждение приходит за раунд ДО разрыва петли: у модели есть chance
// свернуть направление, пока история ещё не схлопнута.
func TestLoopDetectorWarnsOneRoundBeforeBreak(t *testing.T) {
	d := newLoopDetector()
	d.cfg.stateRepeats = 3
	d.cfg.stateWarn = 2

	call := func(grep string) []loopRoundCall {
		return []loopRoundCall{{
			name: "Run", sig: "Run {\"command\":\"npm test | grep " + grep + "\"}", failed: true,
			state: "npm test src/Login.test.tsx", stateSample: "FAIL src/Login.test.tsx: AssertionError",
			stateLabel: "npm test src/Login.test.tsx",
		}}
	}

	d.observeRound(call("-A5"))
	if inj := d.takeInjection(); inj != "" {
		t.Fatalf("после первого падения вмешательства быть не должно, got:\n%s", inj)
	}
	if reason := d.observeRound(call("-B3 -A20")); reason != "" {
		t.Fatalf("на втором падении петля ещё не подтверждена, got %q", reason)
	}

	inj := d.takeInjection()
	if inj == "" {
		t.Fatal("на втором падении подряд должно быть принудительное вмешательство")
	}
	for _, want := range []string{"npm test src/Login.test.tsx", "Login.test.tsx", "waitFor", "не запускай её снова"} {
		if !strings.Contains(strings.ToLower(inj), strings.ToLower(want)) {
			t.Fatalf("вмешательство должно содержать %q, got:\n%s", want, inj)
		}
	}
	// Повторно в той же серии не дублируется.
	if inj := d.takeInjection(); inj != "" {
		t.Fatalf("вмешательство в одной серии должно быть одно, got:\n%s", inj)
	}

	reason := d.observeRound(call("-i console"))
	if !strings.Contains(reason, "состояние проекта не меняется 3 раундов подряд") {
		t.Fatalf("третье падение подряд должно подтвердить петлю, got %q", reason)
	}
	if !strings.Contains(reason, "npm test src/Login.test.tsx") {
		t.Fatalf("причина должна называть проверку, got %q", reason)
	}
}

// Блокировка проверки переживает смену аргументов: тот же запуск с другими
// grep-флагами — та же бесполезная проверка.
func TestLoopBlockedVerifySurvivesArgumentChange(t *testing.T) {
	d := newLoopDetector()
	d.cfg.stateRepeats = 3
	d.cfg.stateWarn = 2

	call := func() []loopRoundCall {
		return []loopRoundCall{{
			name: "Run", sig: "Run {\"command\":\"npm test | grep -A5\"}", failed: true,
			state: "npm test src/Login.test.tsx", stateSample: "FAIL", stateLabel: "npm test | grep -A5",
		}}
	}
	d.observeRound(call())
	d.observeRound(call())
	if inj := d.takeInjection(); inj == "" {
		t.Fatal("ожидалось вмешательство с блокировкой проверки")
	}

	// Тот же запуск, но фильтр вывода другой — должно быть заблокировано.
	if reason, blocked := d.blockedVerifyCall("Run", map[string]any{
		"command": `npm test src/Login.test.tsx 2>&1 | grep -B3 -A20 "console"`,
	}); !blocked {
		t.Fatal("смена grep-флага не должна открывать дорогу тому же перезапуску")
	} else if !strings.Contains(reason, "заблокировал") {
		t.Fatalf("в отказе должно быть объяснение, got %q", reason)
	}

	// Другая проверка и не-проверка — не заблокированы.
	if _, blocked := d.blockedVerifyCall("Run", map[string]any{
		"command": `npm test src/pages/Register.test.tsx 2>&1 | grep -A5`,
	}); blocked {
		t.Fatal("соседний тест не должен блокироваться")
	}
	if _, blocked := d.blockedVerifyCall("WriteFiles", map[string]any{"files": []any{}}); blocked {
		t.Fatal("блокировка проверки не должна касаться прочих инструментов")
	}
}

// Раунд, в котором проверку заблокировал сам харнес, — не «агент перестал
// проверять»: отсчёт неподвижного состояния не сбрасывается, иначе блокировка
// вечно обнуляла бы счётчик и разрыв петли не наступал бы никогда.
func TestLoopBlockedRoundKeepsStateSeries(t *testing.T) {
	d := newLoopDetector()
	d.cfg.stateRepeats = 3
	d.cfg.stateWarn = 2

	failing := func() []loopRoundCall {
		return []loopRoundCall{{
			name: "Run", sig: "Run {\"command\":\"npm test\"}", failed: true,
			state: "npm test src/Login.test.tsx", stateLabel: "npm test src/Login.test.tsx",
		}}
	}
	d.observeRound(failing())
	d.observeRound(failing())
	d.takeInjection()

	// Раунд, где проверка заблокирована: отпечатка нет, состояние не сброшено.
	blocked := []loopRoundCall{{name: "Run", sig: "Run {\"command\":\"npm test\"}", failed: true, blocked: true}}
	if reason := d.observeRound(blocked); reason == "" {
		t.Fatal("блокировка проверки не должна обнулять отсчёт петли")
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
