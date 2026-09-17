package runner

import (
	"ai/agents"
	"ai/runevents"
	"ai/tools"
	"context"
	"strings"
	"testing"
)

// streamProvider — провайдер, поддерживающий стриминг (StreamChatProvider):
// потоково отдаёт текст по кускам через onChunk.
type streamProvider struct {
	chunks []string
}

func (p *streamProvider) ChatOnce(context.Context, agents.Agent, []Message) (*ModelReply, error) {
	return nil, nil // при стриме не должен вызываться
}

func (p *streamProvider) ChatStream(_ context.Context, _ agents.Agent, _ []Message, onChunk func(StreamChunk)) (*ModelReply, error) {
	acc := ""
	for _, c := range p.chunks {
		acc += c
		onChunk(StreamChunk{Partial: acc})
	}
	return &ModelReply{Content: acc, FinishReason: "stop"}, nil
}

// TestGenerateStreamsWhenProviderSupports проверяет, что runner при наличии
// StreamChatProvider идёт стрим-путём: фрагменты (message_delta) перед
// финальным сообщением (message), с общим StreamID.
func TestGenerateStreamsWhenProviderSupports(t *testing.T) {
	var events []runevents.Event
	router := runevents.NewRouter(func(ev runevents.Event) { events = append(events, ev) })
	ctx := runevents.WithReporter(context.Background(), router)

	prov := &streamProvider{chunks: []string{"При", "вет, ", "мир!"}}
	resp, err := Generate(ctx, prov, &fakeAgent{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Content != "Привет, мир!" {
		t.Fatalf("Content = %q", resp.Content)
	}

	var deltas []runevents.Event
	var finals []runevents.Event
	for _, e := range events {
		switch e.Type {
		case runevents.TypeMessageDelta:
			deltas = append(deltas, e)
		case runevents.TypeMessage:
			finals = append(finals, e)
		}
	}
	if len(deltas) != 3 {
		t.Fatalf("message_delta = %d, want 3: %+v", len(deltas), events)
	}
	if deltas[0].StreamID == "" || deltas[0].StreamID != deltas[2].StreamID {
		t.Fatalf("StreamID: %q ≠ %q", deltas[0].StreamID, deltas[2].StreamID)
	}
	if deltas[2].Content != "Привет, мир!" {
		t.Fatalf("последний фрагмент Content = %q", deltas[2].Content)
	}
	if len(finals) != 1 || finals[0].Content != "Привет, мир!" {
		t.Fatalf("финальных сообщений = %+v, want одно полное", finals)
	}
}

// streamTruncatedProvider — стрим-провайдер, завершающийся лимитом длины
// (FinishReason=length): проверяем, что фрагменты шли, а финал помечен
// truncated (флаг приходит в OnMessage).
type streamTruncatedProvider struct{}

func (p *streamTruncatedProvider) ChatOnce(context.Context, agents.Agent, []Message) (*ModelReply, error) {
	return nil, nil
}

func (p *streamTruncatedProvider) ChatStream(_ context.Context, _ agents.Agent, _ []Message, onChunk func(StreamChunk)) (*ModelReply, error) {
	onChunk(StreamChunk{Partial: "обрыв"})
	return &ModelReply{Content: "обрыв", FinishReason: "length"}, nil
}

func TestGenerateStreamTruncatedFinishes(t *testing.T) {
	var events []runevents.Event
	router := runevents.NewRouter(func(ev runevents.Event) { events = append(events, ev) })
	ctx := runevents.WithReporter(context.Background(), router)

	prov := &streamTruncatedProvider{}
	resp, err := Generate(ctx, prov, &fakeAgent{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Content != "обрыв" {
		t.Fatalf("Content = %q", resp.Content)
	}
	if !resp.Truncated {
		t.Fatalf("Expected Truncated, got false")
	}
	var truncated bool
	for _, e := range events {
		if e.Type == runevents.TypeMessage && e.Content == "обрыв" {
			truncated = e.Truncated
		}
	}
	if !truncated {
		t.Fatalf("финальное сообщение не помечено truncated: %+v", events)
	}
}

// Модель «перебирает» аргументы в цикле ошибок: BoardCreateEpic, возвращающий
// ошибку «эпик уже существует», вызывается с РАЗНЫМИ task_id (архитектор
// повторно публикует существующий бэклог). Сигнатурный определитель
// (maxRepeatedToolCalls) такую петлю не ловит — должен сработать per-tool
// счётчик провалов (maxRepeatedToolFails) с подсказкой про обязательное
// действие (submit_architecture_backlog).
func TestGenerateToolFailGuardNudgesRepeatedErrors(t *testing.T) {
	agent := &fakeAgent{
		requiredGroups: [][]string{{"submit_architecture_backlog"}},
		callResults: [][]byte{
			[]byte(`{"status":"error","message":"эпик ARCH-01 уже существует"}`),
			[]byte(`{"status":"error","message":"эпик ARCH-02 уже существует"}`),
			[]byte(`{"status":"error","message":"эпик ARCH-03 уже существует"}`),
			[]byte(`{"status":"error","message":"эпик ARCH-04 уже существует"}`),
		},
	}
	provider := &fakeChatProvider{
		replies: []*ModelReply{
			// раунды 1-4: один и тот же инструмент, разные аргументы (task_id)
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-01"}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-02"}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-03"}`}}, FinishReason: "tool_calls"},
			{ToolCalls: []tools.ToolCall{{Name: "BoardCreateEpic", Arguments: `{"task_id":"ARCH-04"}`}}, FinishReason: "tool_calls"},
			// раунд 5: модель после подсказки вызывает submit_architecture_backlog
			{ToolCalls: []tools.ToolCall{{Name: "submit_architecture_backlog", Arguments: `{}`}}, FinishReason: "tool_calls"},
			// раунд 6: последний — финальный ответ
			{Content: "бэклог уже на доске", FinishReason: "stop"},
		},
	}

	resp := testGenerate(t, agent, provider)

	if resp.Truncated {
		t.Fatal("цикл не должен упереться в лимит раундов: per-tool защита должна прервать перебор")
	}
	if resp.Content != "бэклог уже на доске" {
		t.Fatalf("ожидали эпилог модели, got %q", resp.Content)
	}

	// Подсказка должна появиться ровно один раз, упоминать инструмент и
	// обязательное действие.
	nudges := 0
	for _, m := range resp.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "возвращает ошибку") {
			nudges++
			if !strings.Contains(m.Content, "BoardCreateEpic") {
				t.Fatalf("подсказка должна упоминать BoardCreateEpic, got %q", m.Content)
			}
			if !strings.Contains(m.Content, "submit_architecture_backlog") {
				t.Fatalf("подсказка должна напоминать про обязательный инструмент, got %q", m.Content)
			}
		}
	}
	if nudges < 1 {
		t.Fatalf("нужна подсказка о провалах, но не нашли: %v", resp.Messages)
	}
}

// Проверка вспомогательного сообщения о неудалях без обязательного действия.
func TestToolFailMessageFormat(t *testing.T) {
	msg := toolFailMessage("BoardCreateEpic", 4, "")
	if !strings.Contains(msg, "BoardCreateEpic") || !strings.Contains(msg, "4") {
		t.Fatalf("toolFailMessage = %q", msg)
	}
	if strings.Contains(msg, "Обязательное действие") {
		t.Fatalf("без обязательного действия сообщение не должно упоминать его: %q", msg)
	}
	withReq := toolFailMessage("ReadFiles", 5, "WriteFiles")
	if !strings.Contains(withReq, "WriteFiles") {
		t.Fatalf("с обязательным действием: %q", withReq)
	}
}