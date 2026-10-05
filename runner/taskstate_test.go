package runner

// Тесты State Tracking (Ф-6, этап 2): классификация состояния раунда, сбор
// проверок раунда, усечение вывода падения в last_error и сквозной прогон
// Generate → ReportRoundState.

import (
	"strings"
	"testing"

	"ai/tools"
)

func TestAgentStateFor(t *testing.T) {
	cases := []struct {
		ran, failed bool
		touched     int
		want        string
	}{
		{true, true, 3, "fixing_errors"}, // падение важнее всего: агент чинит
		{true, false, 0, "running_tests"},
		{true, false, 2, "running_tests"}, // проверка приоритетнее «писал код»
		{false, false, 1, "writing_code"},
		{false, false, 0, "idle"},
	}
	for _, c := range cases {
		if got := agentStateFor(c.ran, c.failed, c.touched); got != c.want {
			t.Fatalf("agentStateFor(%v,%v,%d)=%q, ожидалось %q", c.ran, c.failed, c.touched, got, c.want)
		}
	}
}

// roundVerify: упавшая проверка раунда не «забывается» зелёной проверкой
// следом — иначе last_good_sha сдвинулся бы на раунде со сломанным тестом.
func TestRoundVerifyKeepsFailureAfterGreenCheck(t *testing.T) {
	calls := []loopRoundCall{
		{name: "Run", verify: true, verifyCmd: "go test ./...", state: "go test ./...", stateSample: "FAIL pkg/x"},
		{name: "Run", verify: true, verifyCmd: "golangci-lint run"},
		{name: "ReadFiles"},
	}
	ran, failed, cmd, sample := roundVerify(calls)
	if !ran || !failed {
		t.Fatalf("ran=%v failed=%v, ожидались оба true", ran, failed)
	}
	if cmd != "golangci-lint run" {
		t.Fatalf("команда последней проверки: %q", cmd)
	}
	if sample != "FAIL pkg/x" {
		t.Fatalf("образец падения потерян: %q", sample)
	}
}

func TestRoundVerifyNoVerifyInRound(t *testing.T) {
	ran, failed, cmd, sample := roundVerify([]loopRoundCall{{name: "ReadFiles"}, {name: "WriteFiles"}})
	if ran || failed || cmd != "" || sample != "" {
		t.Fatalf("раунд без проверок: ran=%v failed=%v cmd=%q sample=%q", ran, failed, cmd, sample)
	}
}

func TestRoundStateErrorTextTruncates(t *testing.T) {
	raw := strings.Repeat("ошибка ", 1000)
	rs := RoundState{VerifyCommand: "go test ./...", VerifyOutput: raw}
	out := RoundStateErrorText(rs)
	if len(out) >= len(raw) || len(out) > 2*roundStateErrorMax {
		t.Fatalf("вывод не усечён: %d символов из %d", len(out), len(raw))
	}
	if !strings.Contains(out, "go test ./...") {
		t.Fatalf("в last_error нет команды проверки: %q", out[:60])
	}
	if RoundStateErrorText(RoundState{}) != "" {
		t.Fatal("без падения last_error должен быть пустым")
	}
}

// stateAgent — агент, который умеет и сообщать о затронутых файлах, и писать
// состояние раунда (как разработчик с Ф-6).
type stateAgent struct {
	fakeAgent
	touched  [][]string
	states   []RoundState
	commitOK bool
}

func (a *stateAgent) TakeTouched() []string {
	if len(a.touched) == 0 {
		return nil
	}
	out := a.touched[0]
	a.touched = a.touched[1:]
	return out
}

func (a *stateAgent) LspCheckFiles([]string) ([]string, bool) { return nil, false }

func (a *stateAgent) CommitRoundTouched([]string, int) (string, bool, error) {
	if !a.commitOK {
		return "", false, nil
	}
	return "sha" + string(rune('a'+len(a.states))), true, nil
}

func (a *stateAgent) ReportRoundState(rs RoundState) { a.states = append(a.states, rs) }

func TestGenerateReportsRoundStateEachRound(t *testing.T) {
	failRun := []byte(`{"status":"error","exit_error":"exit status 1","stderr":"FAIL pkg/auth\n--- FAIL: TestLogin"}`)
	okRun := []byte(`{"status":"ok","stdout":"ok pkg/auth"}`)
	agent := &stateAgent{
		fakeAgent: fakeAgent{callResults: [][]byte{failRun, okRun}},
		// Раунд 1 — только проверка, раунд 2 — правка файла + зелёная проверка.
		touched:  [][]string{nil, {"server/a.go"}},
		commitOK: true,
	}
	provider := &fakeChatProvider{replies: []*ModelReply{
		{FinishReason: "tool_calls", ToolCalls: []tools.ToolCall{{Name: "Run", Arguments: `{"command":"go test ./..."}`}}},
		{FinishReason: "tool_calls", ToolCalls: []tools.ToolCall{
			{Name: "WriteFiles", Arguments: `{"files":{"server/a.go":"x"}}`},
			{Name: "Run", Arguments: `{"command":"go test ./..."}`},
		}},
		{Content: "готово", FinishReason: "stop"},
	}}
	resp := testGenerate(t, agent, provider)
	if resp.Looped {
		t.Fatalf("цикл прерван: %s", resp.LoopReason)
	}
	if len(agent.states) != 2 {
		t.Fatalf("состояний раундов: %d, ожидалось 2 (%+v)", len(agent.states), agent.states)
	}
	first, second := agent.states[0], agent.states[1]
	if first.State != "fixing_errors" || !first.VerifyFailed {
		t.Fatalf("раунд с упавшим тестом: %+v", first)
	}
	if first.Touched != 0 || first.Committed {
		t.Fatalf("раунд без правок не должен давать коммит: %+v", first)
	}
	if !strings.Contains(first.VerifyOutput, "TestLogin") {
		t.Fatalf("в состояние не попал вывод падения: %q", first.VerifyOutput)
	}
	if second.State != "running_tests" || second.Touched != 1 || !second.Committed {
		t.Fatalf("раунд с правкой и зелёной проверкой: %+v", second)
	}
	if second.CommitSHA == "" {
		t.Fatalf("в состояние не попал SHA коммита: %+v", second)
	}
	if second.Round != 2 {
		t.Fatalf("номер раунда: %d", second.Round)
	}
}

// Агент без RoundStateReporter работает как раньше: отсутствие интерфейса не
// должно ломать генерацию (тихая деградация, а не ошибка).
func TestGenerateWithoutStateReporterStillRuns(t *testing.T) {
	agent := &fakeAgent{callResults: [][]byte{[]byte(`{"status":"ok"}`)}}
	provider := &fakeChatProvider{replies: []*ModelReply{
		{FinishReason: "tool_calls", ToolCalls: []tools.ToolCall{{Name: "Run", Arguments: `{"command":"go build ./..."}`}}},
		{Content: "готово", FinishReason: "stop"},
	}}
	resp := testGenerate(t, agent, provider)
	if resp.Content != "готово" {
		t.Fatalf("ответ: %q", resp.Content)
	}
}
