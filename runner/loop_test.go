package runner

import (
	"ai/tools"
	"context"
	"strings"
	"testing"
)

// Модель повторяет один и тот же вызов: вместо сжигания всех раундов раннер
// разрывает петлю (дайджест + запрет) и даёт шанс ответить с другой стороны.
func TestGenerateBreaksLoopAndModelChangesApproach(t *testing.T) {
	agent := &fakeAgent{}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			// раунды 1-3: один и тот же вызов — детектор подтверждает петлю
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"a.go"}]}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"a.go"}]}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"a.go"}]}`}}, FinishReason: "tool_calls"},
			// раунд 4: после разрыва модель пошла другой дорогой
			{ToolCalls: []tools.ToolCall{{Name: "ReadFiles", Arguments: `{"filenames":["a.go"]}`}}, FinishReason: "tool_calls"},
			// раунд 5: итог
			{Content: "сделал иначе", FinishReason: "stop"},
		},
	}

	resp := testGenerate(t, agent, provider)

	if resp.Looped {
		t.Fatalf("цикл не должен прерываться: модель сменила подход, %s", resp.LoopReason)
	}
	if resp.Content != "сделал иначе" {
		t.Fatalf("ожидали финальный ответ после разрыва петли, got %q", resp.Content)
	}
	if provider.calls != 5 {
		t.Fatalf("ожидали 5 запросов (3 повтора + новый подход + итог), got %d", provider.calls)
	}

	// В историю ушёл разрыв с запретом повторяемого вызова.
	digest := findDigest(resp.Messages)
	if digest == "" {
		t.Fatalf("в истории нет дайджеста разрыва: %#v", resp.Messages)
	}
	for _, want := range []string{"СТОП", "WriteFiles", "С ДРУГОЙ СТОРОНЫ"} {
		if !strings.Contains(digest, want) {
			t.Fatalf("дайджест должен содержать %q, got:\n%s", want, digest)
		}
	}
	// Зацикленные раунды схлопнуты: в истории не осталось повторов
	// (после системного/пользовательского сообщения идёт разрыв и новый подход).
	if repeats := strings.Count(digest, "WriteFiles"); repeats == 0 {
		t.Fatalf("дайджест должен называть повторявшийся инструмент:\n%s", digest)
	}
	last := resp.Messages[len(resp.Messages)-1]
	if last.Role != "assistant" || last.Content != "сделал иначе" {
		t.Fatalf("история должна заканчиваться финальным ответом, got %#v", last)
	}
}

// После разрыва петли модель получает постановку задачи заново и список уже
// тронутых файлов — и действительно начинает с перечитывания, а не продолжает
// «по памяти».
func TestGenerateLoopRestartRepeatsTaskAndTouchedFiles(t *testing.T) {
	agent := &fakeAgent{} // постановка задачи в user-сообщении: «напиши код»
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			// раунды 1-3: пишет один и тот же файл — детектор подтверждает петлю
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"server/api.go","content":"v1"}]}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"server/api.go","content":"v1"}]}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"server/api.go","content":"v1"}]}`}}, FinishReason: "tool_calls"},
			// раунд 4: модель перечитала файл
			{ToolCalls: []tools.ToolCall{{Name: "ReadFiles", Arguments: `{"paths":["server/api.go"]}`}}, FinishReason: "tool_calls"},
			{Content: "починил иначе", FinishReason: "stop"},
		},
	}

	resp := testGenerate(t, agent, provider)
	if resp.Looped {
		t.Fatalf("цикл не должен прерываться: %s", resp.LoopReason)
	}

	digest := findDigest(resp.Messages)
	if digest == "" {
		t.Fatalf("в истории нет разрыва петли: %#v", resp.Messages)
	}
	for _, want := range []string{"НАЧНИ ЗАДАЧУ ЗАНОВО", "напиши код", "server/api.go"} {
		if !strings.Contains(digest, want) {
			t.Fatalf("разрыв должен содержать %q, got:\n%s", want, digest)
		}
	}

	// Перечитывание пришло в том же цикле: после разрыва был вызов чтения.
	if !strings.Contains(digest, "ПЕРВЫЙ ДЕЙСТВИЕ РАУНДА") {
		t.Fatalf("разрыв должен требовать начать с перечитывания, got:\n%s", digest)
	}
	// Вызовы после разрыва: первые три — зацикленные правки, дальше перечитывание.
	var names []string
	for _, tc := range resp.ToolCalls {
		names = append(names, tc.Name)
	}
	if len(names) < 4 || names[3] != "ReadFiles" {
		t.Fatalf("после разрыва модель должна была перечитать файлы, вызовы: %v", names)
	}
}

// Модель не поняла предупреждение и повторяет запрещённый вызов: цикл
// прерывается приговором Looped, бюджет раундов не выжигается.
func TestGenerateReportsLoopedAfterBanIgnored(t *testing.T) {
	agent := &fakeAgent{}
	call := func() *ModelReply {
		return &ModelReply{
			FinishReason: "tool_calls",
			ToolCalls:    []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"a.go"}]}`}},
		}
	}
	provider := &fakeChatProvider{replies: []*ModelReply{call()}}

	resp := testGenerate(t, agent, provider)

	if !resp.Looped {
		t.Fatalf("цикл должен быть прерван как зацикливание, Truncated=%v", resp.Truncated)
	}
	if resp.LoopReason == "" {
		t.Fatal("в приговоре должна быть причина зацикливания")
	}
	if !strings.Contains(resp.LoopReason, "WriteFiles") {
		t.Fatalf("причина должна называть повторённый вызов, got %q", resp.LoopReason)
	}
	if resp.Truncated {
		t.Fatal("петля не должна выглядеть как исчерпание лимита раундов")
	}
	if provider.calls > maxRepeatedToolCalls*2 {
		t.Fatalf("петля сожгла слишком много раундов: %d", provider.calls)
	}
}

// Петля из одних ошибок (архитектор пересоздаёт существующие эпики) тоже
// приводит к разрыву: дайджест перечисляет инструмент и его ошибку.
func TestGenerateBreaksLoopOnRepeatedToolErrors(t *testing.T) {
	agent := &fakeAgent{
		requiredGroups: [][]string{{"submit_architecture_backlog"}},
		callResults: [][]byte{
			[]byte(`{"status":"error","message":"эпик ARCH-01 уже существует"}`),
			[]byte(`{"status":"error","message":"эпик ARCH-02 уже существует"}`),
			[]byte(`{"status":"error","message":"эпик ARCH-03 уже существует"}`),
			[]byte(`{"status":"error","message":"эпик ARCH-04 уже существует"}`),
			[]byte(`{"status":"error","message":"эпик ARCH-05 уже существует"}`),
		},
	}
	// Разные task_id: повторов по сигнатуре нет — запрещается инструмент.
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-01"}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-02"}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-03"}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-04"}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-05"}`}}, FinishReason: "tool_calls"},
			// после разрыва модель публикует бэклог
			{ToolCalls: []tools.ToolCall{{Name: "submit_architecture_backlog", Arguments: `{}`}}, FinishReason: "tool_calls"},
			{Content: "бэклог опубликован", FinishReason: "stop"},
		},
	}

	resp := testGenerate(t, agent, provider)

	if resp.Looped {
		t.Fatalf("цикл не должен прерываться: после разрыва модель выполнила обязательное действие (%s)", resp.LoopReason)
	}
	digest := findDigest(resp.Messages)
	if digest == "" {
		t.Fatalf("в истории нет дайджеста разрыва: %#v", resp.Messages)
	}
	if !strings.Contains(digest, "BoardCreateEpic") {
		t.Fatalf("дайджест должен называть инструмент в петле, got:\n%s", digest)
	}
	if !strings.Contains(digest, "submit_architecture_backlog") {
		t.Fatalf("дайджест должен напомнить обязательное действие, got:\n%s", digest)
	}
}

// Разрыв не теряет постановку задачи: без неё модель начнёт «с другой
// стороны» от неизвестно чего.
func TestGenerateBreakKeepsTaskStatement(t *testing.T) {
	agent := &fakeAgent{}
	call := &ModelReply{
		FinishReason: "tool_calls",
		ToolCalls:    []tools.ToolCall{{Name: "WriteFiles", Arguments: `{"files":[{"filename":"a.go"}]}`}},
	}
	provider := &fakeChatProvider{replies: []*ModelReply{call, call, call, call, call, call}}

	resp := testGenerate(t, agent, provider)

	if len(resp.Messages) < 2 {
		t.Fatalf("история слишком короткая: %#v", resp.Messages)
	}
	if resp.Messages[0].Role != "system" || resp.Messages[0].Content != "ты агент" {
		t.Fatalf("системный промпт должен сохраниться, got %#v", resp.Messages[0])
	}
	if resp.Messages[1].Role != "user" || resp.Messages[1].Content != "напиши код" {
		t.Fatalf("постановка задачи должна сохраниться, got %#v", resp.Messages[1])
	}
}

// findDigest возвращает текст дайджеста разрыва петли из истории диалога.
func findDigest(messages []Message) string {
	for _, m := range messages {
		if m.Role == "user" && strings.Contains(m.Content, "СТОП: ты попал в повторяющийся цикл") {
			return m.Content
		}
	}
	return ""
}

// StopReason/LoopError — единая точка правды для вызывающего кода: зацикливание
// и лимит раундов различаются, но оба означают «задача не выполнена».
func TestAgentResponseStopReasonAndLoopError(t *testing.T) {
	if got := (*AgentResponse)(nil).StopReason(); got != "" {
		t.Fatalf("nil-ответ не остановлен, got %q", got)
	}
	if err := (*AgentResponse)(nil).LoopError("фаза"); err != nil {
		t.Fatalf("nil-ответ без петли не даёт ошибки, got %v", err)
	}

	looped := &AgentResponse{Looped: true, LoopReason: "повтор Run"}
	if got := looped.StopReason(); got != "зацикливание" {
		t.Fatalf("StopReason = %q", got)
	}
	err := looped.LoopError("фаза архитектора")
	if err == nil {
		t.Fatal("зацикливание должно давать ошибку вызывающему коду")
	}
	if !strings.Contains(err.Error(), "фаза архитектора") || !strings.Contains(err.Error(), "повтор Run") {
		t.Fatalf("ошибка должна нести контекст и причину, got %v", err)
	}

	trunc := &AgentResponse{Truncated: true}
	if got := trunc.StopReason(); got != "лимит раундов" {
		t.Fatalf("StopReason = %q", got)
	}
	if err := trunc.LoopError("фаза"); err != nil {
		t.Fatalf("лимит раундов — не петля, ошибки быть не должно: %v", err)
	}
	ok := &AgentResponse{Content: "готово"}
	if got := ok.StopReason(); got != "" {
		t.Fatalf("успешный ответ не остановлен, got %q", got)
	}
}

// WithoutResumeState: задача, взятая «с другой стороны», не продолжает
// застрявший диалог — цикл стартует заново.
func TestWithoutResumeState(t *testing.T) {
	ctx := WithResumeState(context.Background(), &ResumeState{Rounds: 42, Messages: []Message{{Role: "system", Content: "s"}}})
	if st := ResumeStateFromContext(ctx); st == nil || st.Rounds != 42 {
		t.Fatalf("resume-состояние должно читаться, got %#v", st)
	}
	if st := ResumeStateFromContext(WithoutResumeState(ctx)); st != nil {
		t.Fatalf("после сброса resume-состояния быть не должно, got %#v", st)
	}
	if st := ResumeStateFromContext(context.Background()); st != nil {
		t.Fatalf("без resume-состояния nil, got %#v", st)
	}
}
