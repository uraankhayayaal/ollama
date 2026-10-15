package ask

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"ai/domain"
)

// recAnnouncer — заглушка Announcer: копит опубликованные вопросы, статусы и
// логи.
type recAnnouncer struct {
	mu     sync.Mutex
	asks   []domain.Ask
	status []string
	info   []string
}

func (r *recAnnouncer) Ask(a domain.Ask) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asks = append(r.asks, a)
}

func (r *recAnnouncer) Status(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = append(r.status, msg)
}

func (r *recAnnouncer) Info(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.info = append(r.info, msg)
}

func (r *recAnnouncer) lastAsk() domain.Ask {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.asks) == 0 {
		return domain.Ask{}
	}
	return r.asks[len(r.asks)-1]
}

func TestParseQuestionsValid(t *testing.T) {
	qs, err := ParseQuestions(map[string]any{
		"questions": []any{
			map[string]any{
				"id":   "q1",
				"text": "Какой язык?",
				"kind": "single",
				"options": []any{
					map[string]any{"id": "go", "label": "Go", "recommended": true},
					map[string]any{"id": "rust", "label": "Rust"},
				},
			},
			map[string]any{
				"id":           "q2",
				"text":         "Какие фичи?",
				"kind":         "multi",
				"allow_custom": false,
				"options": []any{
					map[string]any{"id": "a", "label": "А"},
					map[string]any{"id": "b", "label": "Б"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ParseQuestions: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("len = %d", len(qs))
	}
	if qs[0].ID != "q1" || qs[0].Kind != domain.AskSingle || !qs[0].AllowCustom {
		t.Fatalf("q0 = %+v", qs[0])
	}
	if !qs[0].Options[0].Recommended || qs[0].Options[1].Recommended {
		t.Fatalf("recommended = %+v", qs[0].Options)
	}
	if qs[1].Kind != domain.AskMulti || qs[1].AllowCustom {
		t.Fatalf("q1 = %+v", qs[1])
	}
}

func TestParseQuestionsDefaults(t *testing.T) {
	qs, err := ParseQuestions(map[string]any{
		"questions": []any{
			map[string]any{"id": "q", "text": "?", "options": []any{map[string]any{"id": "a", "label": "А"}}},
		},
	})
	if err != nil {
		t.Fatalf("ParseQuestions: %v", err)
	}
	if qs[0].Kind != domain.AskSingle {
		t.Fatalf("kind = %q, want single", qs[0].Kind)
	}
	if !qs[0].AllowCustom {
		t.Fatalf("allow_custom должен быть true по умолчанию")
	}
}

func TestParseQuestionsErrors(t *testing.T) {
	cases := map[string]map[string]any{
		"пусто":       {},
		"нет текста":  {"questions": []any{map[string]any{"id": "q", "kind": "single", "options": []any{map[string]any{"id": "a", "label": "А"}}}}},
		"нет опций":   {"questions": []any{map[string]any{"id": "q", "text": "?", "kind": "single", "options": []any{}}}},
		"плохой kind": {"questions": []any{map[string]any{"id": "q", "text": "?", "kind": "yesno", "options": []any{map[string]any{"id": "a", "label": "А"}}}}},
		"дубль id": {"questions": []any{
			map[string]any{"id": "q", "text": "?", "kind": "single", "options": []any{map[string]any{"id": "a", "label": "А"}}},
			map[string]any{"id": "q", "text": "!", "kind": "single", "options": []any{map[string]any{"id": "b", "label": "Б"}}},
		}},
		"дубль опции": {"questions": []any{map[string]any{"id": "q", "text": "?", "kind": "single", "options": []any{
			map[string]any{"id": "a", "label": "А"},
			map[string]any{"id": "a", "label": "Б"},
		}}}},
	}
	for name, args := range cases {
		if _, err := ParseQuestions(args); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
}

func TestPendingAskBlocksUntilAnswered(t *testing.T) {
	ann := &recAnnouncer{}
	p := NewPending(ann)
	questions := []domain.AskQuestion{
		{ID: "q1", Text: "Язык?", Kind: domain.AskSingle, Options: []domain.AskOption{{ID: "go", Label: "Go"}}},
		{ID: "q2", Text: "Фичи?", Kind: domain.AskMulti, Options: []domain.AskOption{{ID: "a", Label: "А"}}},
	}

	type out struct {
		ans []domain.AskAnswer
		err error
	}
	done := make(chan out, 1)
	go func() {
		ans, err := p.Ask(context.Background(), questions)
		done <- out{ans, err}
	}()

	ask := waitAsk(t, ann)
	if ask.ID == "" {
		t.Fatal("ask ID пустой")
	}

	select {
	case o := <-done:
		t.Fatalf("Ask завершился до ответа: %+v", o)
	default:
	}

	if answered, total, err := p.Answer(ask.ID, "q1", []string{"go"}, ""); err != nil || answered != 1 || total != 2 {
		t.Fatalf("answer q1 = %d/%d err=%v", answered, total, err)
	}
	select {
	case o := <-done:
		t.Fatalf("Ask завершился после 1/2: %+v", o)
	default:
	}

	if answered, total, err := p.Answer(ask.ID, "q2", []string{"a"}, "свой"); err != nil || answered != 2 || total != 2 {
		t.Fatalf("answer q2 = %d/%d err=%v", answered, total, err)
	}

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("Ask: %v", o.err)
		}
		if len(o.ans) != 2 || o.ans[0].QuestionID != "q1" || o.ans[1].QuestionID != "q2" {
			t.Fatalf("порядок ответов: %+v", o.ans)
		}
		if o.ans[1].Custom != "свой" {
			t.Fatalf("custom = %q", o.ans[1].Custom)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ask не разблокировался")
	}

	ann.mu.Lock()
	defer ann.mu.Unlock()
	if len(ann.status) != 1 || !strings.Contains(ann.status[0], "Получены ответы") {
		t.Fatalf("статус = %+v", ann.status)
	}
}

func TestPendingGuardAndValidation(t *testing.T) {
	ann := &recAnnouncer{}
	p := NewPending(ann)
	qs := []domain.AskQuestion{{ID: "q", Text: "?", Kind: domain.AskSingle, Options: []domain.AskOption{{ID: "a", Label: "А"}}}}

	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_, err := p.Ask(ctx, qs)
		done <- err
	}()
	ask := waitAsk(t, ann)

	if _, err := p.Ask(ctx, qs); err == nil {
		t.Fatal("повторный Ask должен падать")
	}
	if _, _, err := p.Answer("ask-nope", "q", nil, ""); err == nil {
		t.Fatal("неверный askID должен падать")
	}
	if _, _, err := p.Answer(ask.ID, "q", []string{"nope"}, ""); err == nil {
		t.Fatal("несуществующий вариант должен падать")
	}
	if _, _, err := p.Answer(ask.ID, "q-wrong", []string{"a"}, ""); err == nil {
		t.Fatal("несуществующий вопрос должен падать")
	}
	if _, _, err := p.Answer(ask.ID, "q", []string{"a"}, ""); err != nil {
		t.Fatalf("полный ответ: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ask не разблокировался")
	}
	if _, _, err := p.Answer(ask.ID, "q", []string{"a"}, ""); err == nil {
		t.Fatal("ответ на закрытый вопрос должен падать")
	}
}

func TestPendingAskContextCancel(t *testing.T) {
	ann := &recAnnouncer{}
	p := NewPending(ann)
	var n int
	p.newID = func() string { n++; return "ask-test-" + string(rune('0'+n)) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Ask(ctx, []domain.AskQuestion{{ID: "q", Text: "?", Kind: domain.AskSingle, Options: []domain.AskOption{{ID: "a", Label: "А"}}}})
		done <- err
	}()
	ask := waitAsk(t, ann)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Ask должен вернуть ошибку контекста")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ask не отменился по контексту")
	}
	// После отмены активный вопрос снят — можно задать новый.
	done2 := make(chan error, 1)
	go func() {
		_, err := p.Ask(context.Background(), []domain.AskQuestion{{ID: "q", Text: "?", Kind: domain.AskSingle, Options: []domain.AskOption{{ID: "a", Label: "А"}}}})
		done2 <- err
	}()
	ask2 := waitAskCount(t, ann, 2)
	if ask2.ID == ask.ID {
		t.Fatalf("id нового вопроса совпал со старым: %s", ask2.ID)
	}
	if _, _, err := p.Answer(ask2.ID, "q", []string{"a"}, ""); err != nil {
		t.Fatalf("ответ после отмены: %v", err)
	}
	<-done2
}

func TestSummarize(t *testing.T) {
	qs := []domain.AskQuestion{
		{ID: "q1", Text: "Язык?", Options: []domain.AskOption{{ID: "go", Label: "Go"}, {ID: "rust", Label: "Rust"}}},
		{ID: "q2", Text: "Фичи?", Options: []domain.AskOption{{ID: "a", Label: "Авторизация"}}},
	}
	ans := []domain.AskAnswer{
		{QuestionID: "q1", Selected: []string{"go"}},
		{QuestionID: "q2", Custom: "своё"},
	}
	got := Summarize(qs, ans)
	if !strings.Contains(got, "Go") || !strings.Contains(got, "свой ответ: своё") {
		t.Fatalf("Summarize = %q", got)
	}
}

// waitAsk ждёт первую публикацию вопроса от Announcer.
func waitAsk(t *testing.T, ann *recAnnouncer) domain.Ask {
	t.Helper()
	return waitAskCount(t, ann, 1)
}

// waitAskCount ждёт появления как минимум n опубликованных вопросов.
func waitAskCount(t *testing.T, ann *recAnnouncer, n int) domain.Ask {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ann.mu.Lock()
		got := len(ann.asks)
		ann.mu.Unlock()
		if got >= n {
			return ann.lastAsk()
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("вопрос не опубликован (нужно %d)", n)
	return domain.Ask{}
}
