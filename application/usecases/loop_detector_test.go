package usecases

import (
	"strings"
	"testing"
)

// readOnlyLikeProduction повторяет классификацию инструментов чтения:
// в проде её задаёт runner (tools.IsParallelSafe).
func readOnlyLikeProduction(name string) bool {
	switch name {
	case "List", "ReadFiles", "ReadMap":
		return true
	}
	return false
}

func TestVerifyIdentityIgnoresPipelineAndFlags(t *testing.T) {
	same := []string{
		`cd frontend && npm test -- --run src/pages/Login.test.tsx 2>&1 | grep -B5 -A30 "FAIL\|Error"`,
		`cd frontend && npm test -- --run src/pages/Login.test.tsx 2>&1 | grep -A5 "stderr"`,
		`cd /home/user/app/temp/.wt-task-42/frontend && npm test -- --run src/pages/Login.test.tsx 2>&1 | grep -i "console\|warn" | tail -20`,
		`cd frontend && npm test -- --run src/pages/Login.test.tsx`,
	}
	want := VerifyIdentity(same[0])
	if want != "cd … && npm test src/pages/Login.test.tsx" {
		t.Fatalf("неожиданная идентичность: %q", want)
	}
	for _, cmd := range same[1:] {
		if got := VerifyIdentity(cmd); got != want {
			t.Fatalf("фильтр вывода не должен менять идентичность проверки:\n%q\n%q != %q", cmd, got, want)
		}
	}

	// Разная проверка — разная идентичность: соседние тесты не должны слиться.
	other := VerifyIdentity(`cd frontend && npm test -- --run src/pages/Register.test.tsx 2>&1 | grep FAIL`)
	if other == want {
		t.Fatal("разные тесты должны давать разные отпечатки состояния")
	}
}

// Ключевой случай жалобы: агент каждый раунд делает формально новый вызов
// (другие строки/ключи), но состояние проекта и вывод падения — те же. Детектор
// обязан увидеть петлю по состоянию, а не по сигнатуре.
func TestLoopDetectorDetectsUnchangedStateWithVaryingCalls(t *testing.T) {
	d := NewLoopDetector(readOnlyLikeProduction)
	d.cfg.sigRepeats = 50 // сигнатуры «разные» — их не ловим
	d.cfg.failStreak = 50
	d.cfg.noWrite = 50

	var reason string
	for i := 0; i < 3; i++ {
		calls := []LoopRoundCall{
			{Name: "ReadFiles", Sig: "ReadFiles {\"paths\":[\"a" + string(rune('a'+i)) + ".go\"]}"},
			{Name: "Run", Sig: "Run {\"command\":\"make frontend-test " + string(rune('a'+i)) + "\"}",
				Failed: true, State: "deadbeef", StateSample: "Tests: 1 failed", StateLabel: "make frontend-test"},
		}
		reason = d.ObserveRound(calls)
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
	d := NewLoopDetector(nil)
	d.cfg.stateRepeats = 3

	failing := func() []LoopRoundCall {
		return []LoopRoundCall{{Name: "Run", Sig: "Run {\"command\":\"make test\"}", Failed: true, State: "s1"}}
	}
	d.ObserveRound(failing())
	d.ObserveRound(failing())
	// Правка прошла успешно — состояние другое.
	d.ObserveRound([]LoopRoundCall{{Name: "WriteFiles", Sig: "WriteFiles {\"files\":1}", Failed: false}})
	d.ObserveRound(failing())

	if d.stateRuns["s1"] != 1 {
		t.Fatalf("после успешной правки отсчёт состояния должен начаться заново, got %d", d.stateRuns["s1"])
	}
}

// Verify guard (сеть на случай правок при том же падении): N одинаковых
// падений проверки подряд включают вмешательство и временную блокировку
// повторного запуска.
func TestLoopVerifyGuardInjectsAndBlocks(t *testing.T) {
	d := NewLoopDetector(nil)
	d.cfg.verifyFails = 5
	d.cfg.stateWarn = 0 // здесь проверяем именно сеть поверх правок

	call := func() []LoopRoundCall {
		return []LoopRoundCall{{
			Name: "Run", Sig: "Run {\"command\":\"make frontend-test\"}", Failed: true,
			State: "make frontend-test", StateSample: "Tests: 1 failed, Login.test.tsx:12", StateLabel: "make frontend-test",
		}}
	}

	for i := 1; i < 5; i++ {
		d.ObserveRound(call())
		if inj := d.TakeInjection(); inj != "" {
			t.Fatalf("вмешательство слишком рано: раунд %d", i)
		}
	}
	d.ObserveRound(call())

	injection := d.TakeInjection()
	if injection == "" {
		t.Fatal("после 5 одинаковых падений должно быть принудительное вмешательство")
	}
	for _, want := range []string{"make frontend-test", "Login.test.tsx", "waitFor", "не запускай её снова"} {
		if !strings.Contains(strings.ToLower(injection), strings.ToLower(want)) {
			t.Fatalf("вмешательство должно содержать %q, got:\n%s", want, injection)
		}
	}

	// Повторный запуск той же проверки заблокирован, чужой вызов — нет.
	if reason, blocked := d.BlockedVerifyCall("Run", map[string]any{"command": "make frontend-test"}); !blocked {
		t.Fatal("повторный запуск падавшей проверки должен быть заблокирован")
	} else if !strings.Contains(reason, "заблокировал") {
		t.Fatalf("в отказе должно быть объяснение, got %q", reason)
	}
	if _, blocked := d.BlockedVerifyCall("Run", map[string]any{"command": "git status"}); blocked {
		t.Fatal("блокировка не должна затрагивать другие команды")
	}

	// Блокировка живёт ровно LOOP_VERIFY_BLOCK_ROUNDS раундов.
	for i := 0; i < d.cfg.verifyBlockRounds; i++ {
		d.ExpireBlocks()
	}
	if _, blocked := d.BlockedVerifyCall("Run", map[string]any{"command": "make frontend-test"}); blocked {
		t.Fatal("после истечения срока блокировка должна сняться")
	}
}

// Вмешательство не является разрывом петли: бюджет разрывов не тратится, но
// повториться может.
func TestLoopVerifyGuardResetsAfterInjection(t *testing.T) {
	d := NewLoopDetector(nil)
	d.cfg.verifyFails = 2
	d.cfg.verifyBlockRounds = 0
	d.cfg.stateWarn = 0

	call := func() []LoopRoundCall {
		return []LoopRoundCall{{Name: "Run", Sig: "Run {\"command\":\"make test\"}", Failed: true, State: "h", StateLabel: "make test"}}
	}
	for i := 0; i < 6; i++ {
		d.ObserveRound(call())
	}

	if d.breaks != 0 {
		t.Fatalf("verify guard не должен тратить разрывы петли, got %d", d.breaks)
	}
	if inj := d.TakeInjection(); inj == "" {
		t.Fatal("вмешательства должны повторяться")
	}
}

// Предупреждение приходит за раунд ДО разрыва петли: у модели есть chance
// свернуть направление, пока история ещё не схлопнута.
func TestLoopDetectorWarnsOneRoundBeforeBreak(t *testing.T) {
	d := NewLoopDetector(nil)
	d.cfg.stateRepeats = 3
	d.cfg.stateWarn = 2

	call := func(grep string) []LoopRoundCall {
		return []LoopRoundCall{{
			Name: "Run", Sig: "Run {\"command\":\"npm test | grep " + grep + "\"}", Failed: true,
			State: "npm test src/Login.test.tsx", StateSample: "FAIL src/Login.test.tsx: AssertionError",
			StateLabel: "npm test src/Login.test.tsx",
		}}
	}

	d.ObserveRound(call("-A5"))
	if inj := d.TakeInjection(); inj != "" {
		t.Fatalf("после первого падения вмешательства быть не должно, got:\n%s", inj)
	}
	if reason := d.ObserveRound(call("-B3 -A20")); reason != "" {
		t.Fatalf("на втором падении петля ещё не подтверждена, got %q", reason)
	}

	inj := d.TakeInjection()
	if inj == "" {
		t.Fatal("на втором падении подряд должно быть принудительное вмешательство")
	}
	for _, want := range []string{"npm test src/Login.test.tsx", "Login.test.tsx", "waitFor", "не запускай её снова"} {
		if !strings.Contains(strings.ToLower(inj), strings.ToLower(want)) {
			t.Fatalf("вмешательство должно содержать %q, got:\n%s", want, inj)
		}
	}
	// Повторно в той же серии не дублируется.
	if inj := d.TakeInjection(); inj != "" {
		t.Fatalf("вмешательство в одной серии должно быть одно, got:\n%s", inj)
	}

	reason := d.ObserveRound(call("-i console"))
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
	d := NewLoopDetector(nil)
	d.cfg.stateRepeats = 3
	d.cfg.stateWarn = 2

	call := func() []LoopRoundCall {
		return []LoopRoundCall{{
			Name: "Run", Sig: "Run {\"command\":\"npm test | grep -A5\"}", Failed: true,
			State: "npm test src/Login.test.tsx", StateSample: "FAIL", StateLabel: "npm test | grep -A5",
		}}
	}
	d.ObserveRound(call())
	d.ObserveRound(call())
	if inj := d.TakeInjection(); inj == "" {
		t.Fatal("ожидалось вмешательство с блокировкой проверки")
	}

	// Тот же запуск, но фильтр вывода другой — должно быть заблокировано.
	if reason, blocked := d.BlockedVerifyCall("Run", map[string]any{
		"command": `npm test src/Login.test.tsx 2>&1 | grep -B3 -A20 "console"`,
	}); !blocked {
		t.Fatal("смена grep-флага не должна открывать дорогу тому же перезапуску")
	} else if !strings.Contains(reason, "заблокировал") {
		t.Fatalf("в отказе должно быть объяснение, got %q", reason)
	}

	// Другая проверка и не-проверка — не заблокированы.
	if _, blocked := d.BlockedVerifyCall("Run", map[string]any{
		"command": `npm test src/pages/Register.test.tsx 2>&1 | grep -A5`,
	}); blocked {
		t.Fatal("соседний тест не должен блокироваться")
	}
	if _, blocked := d.BlockedVerifyCall("WriteFiles", map[string]any{"files": []any{}}); blocked {
		t.Fatal("блокировка проверки не должна касаться прочих инструментов")
	}
}

// Раунд, в котором проверку заблокировал сам харнес, — не «агент перестал
// проверять»: отсчёт неподвижного состояния не сбрасывается, иначе блокировка
// вечно обнуляла бы счётчик и разрыв петли не наступал бы никогда.
func TestLoopBlockedRoundKeepsStateSeries(t *testing.T) {
	d := NewLoopDetector(nil)
	d.cfg.stateRepeats = 3
	d.cfg.stateWarn = 2

	failing := func() []LoopRoundCall {
		return []LoopRoundCall{{
			Name: "Run", Sig: "Run {\"command\":\"npm test\"}", Failed: true,
			State: "npm test src/Login.test.tsx", StateLabel: "npm test src/Login.test.tsx",
		}}
	}
	d.ObserveRound(failing())
	d.ObserveRound(failing())
	d.TakeInjection()

	// Раунд, где проверка заблокирована: отпечатка нет, состояние не сброшено.
	blocked := []LoopRoundCall{{Name: "Run", Sig: "Run {\"command\":\"npm test\"}", Failed: true, Blocked: true}}
	if reason := d.ObserveRound(blocked); reason == "" {
		t.Fatal("блокировка проверки не должна обнулять отсчёт петли")
	}
}
