package chat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func newTestStore(t *testing.T, project string) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	store := NewStoreNoCheck(StoreConfig{Addr: mr.Addr(), Project: project})
	t.Cleanup(func() { _ = store.Close() })
	return store, mr
}

func boolPtr(v bool) *bool { return &v }

func TestAppendAndHistory(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t, "proj-a")

	id1, err := s.Append(ctx, Message{Role: RoleUser, Content: "сделай x"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := s.Append(ctx, Message{Role: RoleAssistant, Agent: "architect", Content: "план"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, Message{Role: RoleTool, Agent: "developer", Tool: "Write", OK: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}

	hist, err := s.History(ctx, 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("History: %d сообщений, want 3", len(hist))
	}
	if hist[0].Role != RoleUser || hist[0].Content != "сделай x" || hist[0].ID != id1 {
		t.Fatalf("hist[0] = %+v", hist[0])
	}
	if hist[1].Agent != "architect" || hist[1].Role != RoleAssistant {
		t.Fatalf("hist[1] = %+v", hist[1])
	}
	if hist[2].Tool != "Write" || hist[2].OK == nil || !*hist[2].OK {
		t.Fatalf("hist[2] = %+v", hist[2])
	}
	if hist[0].Time.IsZero() {
		t.Fatal("пустое время")
	}
}

func TestHistoryLimitAndOrder(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t, "proj-b")
	for i := 0; i < 5; i++ {
		if _, err := s.Append(ctx, Message{Role: RoleUser, Content: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	hist, err := s.History(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("History(3): %d, want 3", len(hist))
	}
	// Хронологический порядок: старые ID строго меньше новых.
	for i := 1; i < len(hist); i++ {
		if hist[i].ID <= hist[i-1].ID {
			t.Fatalf("порядок нарушен: %s после %s", hist[i].ID, hist[i-1].ID)
		}
	}
}

// TestClearWipesHistory — Clear стирает стрим целиком («кофе-брейк»): после
// очистки история пуста, а новые сообщения Append записываются заново.
func TestClearWipesHistory(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t, "proj-clear")

	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, Message{Role: RoleUser, Content: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	if hist, err := s.History(ctx, 10); err != nil || len(hist) != 3 {
		t.Fatalf("история до очистки: %v, count=%d (want 3)", err, len(hist))
	}

	if err := s.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	hist, err := s.History(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 0 {
		t.Fatalf("после Clear история не пуста: %d сообщений", len(hist))
	}

	// Стрим жив: новые сообщения записываются, числа сообщений идут с нуля.
	first, err := s.Append(ctx, Message{Role: RoleUser, Content: "с нуля"})
	if err != nil {
		t.Fatal(err)
	}
	hist, err = s.History(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].ID != first || hist[0].Content != "с нуля" {
		t.Fatalf("после Clear новая запись = %+v", hist)
	}
}

func TestSubscribeLive(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t, "proj-c")

	ps, err := s.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer ps.Close()

	want := Message{Role: RoleStatus, Content: "агент начал работу", Agent: "backendlead"}
	if _, err := s.Append(ctx, want); err != nil {
		t.Fatal(err)
	}

	done := make(chan Message, 1)
	go func() {
		msg, err := ps.ReceiveMessage(ctx)
		if err != nil {
			return
		}
		var got Message
		if json.Unmarshal([]byte(msg.Payload), &got) == nil {
			done <- got
		}
	}()

	select {
	case got := <-done:
		if got.Role != RoleStatus || got.Agent != "backendlead" || got.Content != "агент начал работу" {
			t.Fatalf("live = %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("не пришло live-событие")
	}
}

func TestOpenRequiresProject(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	if _, err := NewStore(context.Background(), StoreConfig{Addr: mr.Addr()}); err == nil {
		t.Fatal("NewStore без проекта должен падать")
	}
}

// TestAskPersistence — структурированный вопрос (роль ask + payload Ask)
// сохраняется в истории и восстанавливается из неё целиком (план «спроси
// пользователя при неоднозначности», Ф-1).
func TestAskPersistence(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t, "proj-ask")

	ask := &Ask{
		ID: "ask-1",
		Questions: []AskQuestion{
			{
				ID:   "q1",
				Text: "Какой язык для порта?",
				Kind: AskSingle,
				Options: []AskOption{
					{ID: "go", Label: "Go", Recommended: true},
					{ID: "rust", Label: "Rust"},
				},
			},
			{
				ID:          "q2",
				Text:        "Какие фичи добавить?",
				Kind:        AskMulti,
				AllowCustom: true,
				Options: []AskOption{
					{ID: "a", Label: "Авторизация"},
					{ID: "b", Label: "Логи"},
				},
			},
		},
	}
	if _, err := s.Append(ctx, Message{Role: RoleAsk, Ask: ask}); err != nil {
		t.Fatalf("Append(ask): %v", err)
	}

	hist, err := s.History(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 {
		t.Fatalf("History: %d сообщений, want 1", len(hist))
	}
	got := hist[0]
	if got.Role != RoleAsk {
		t.Fatalf("role = %q, want ask", got.Role)
	}
	if got.Ask == nil {
		t.Fatal("Ask payload не восстановлен")
	}
	if got.Ask.ID != "ask-1" || len(got.Ask.Questions) != 2 {
		t.Fatalf("Ask = %+v", got.Ask)
	}
	q1 := got.Ask.Questions[0]
	if q1.ID != "q1" || q1.Kind != AskSingle || len(q1.Options) != 2 || !q1.Options[0].Recommended {
		t.Fatalf("q1 = %+v", q1)
	}
	q2 := got.Ask.Questions[1]
	if q2.Kind != AskMulti || !q2.AllowCustom {
		t.Fatalf("q2 = %+v", q2)
	}
}

// TestAskMarshalsInLiveEvent — структурированный вопрос попадает в live-событие
// (pub/sub) вместе с payload Ask: фронт получит карточку вопроса без доп. REST.
func TestAskMarshalsInLiveEvent(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t, "proj-ask-live")

	ps, err := s.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer ps.Close()

	ask := &Ask{
		ID: "ask-9",
		Questions: []AskQuestion{
			{ID: "q", Text: "Продолжить?", Kind: AskSingle, Options: []AskOption{{ID: "y", Label: "Да"}}},
		},
	}
	if _, err := s.Append(ctx, Message{Role: RoleAsk, Ask: ask}); err != nil {
		t.Fatal(err)
	}

	done := make(chan Message, 1)
	go func() {
		msg, err := ps.ReceiveMessage(ctx)
		if err != nil {
			return
		}
		var got Message
		if json.Unmarshal([]byte(msg.Payload), &got) == nil && got.Ask != nil {
			done <- got
		}
	}()

	select {
	case got := <-done:
		if got.Ask.ID != "ask-9" || len(got.Ask.Questions) != 1 || got.Ask.Questions[0].Options[0].ID != "y" {
			t.Fatalf("live ask = %+v", got.Ask)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("live-событие с Ask не пришло")
	}
}
