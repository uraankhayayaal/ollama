// Тесты инструмента AskUser и серверной части структурированных вопросов
// (Ф-1 «спроси пользователя»): парсинг аргументов, определение схемы, блоки-
// рующий цикл AskUser↔AnswerAsk на реальной сессии (miniredis), REST-маршрут.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai/chat"
	"ai/runner"
	"ai/tools"
)

// fakeAskBackend — заглушка AskBackend: отвечает сразу, без блокировки.
type fakeAskBackend struct {
	gotQuestions []chat.AskQuestion
	answers      []chat.AskAnswer
	err          error
}

func (f *fakeAskBackend) AskUser(_ context.Context, questions []chat.AskQuestion) ([]chat.AskAnswer, error) {
	f.gotQuestions = questions
	if f.err != nil {
		return nil, f.err
	}
	return f.answers, nil
}

// TestAskUserToolParsesAndAnswers — инструмент разбирает пачку вопросов,
// вызывает backend и возвращает ответы модели в JSON (status=answered).
func TestAskUserToolParsesAndAnswers(t *testing.T) {
	tool := &askTool{b: &fakeAskBackend{
		answers: []chat.AskAnswer{
			{QuestionID: "q1", Selected: []string{"go"}},
			{QuestionID: "q2", Selected: []string{"a", "b"}, Custom: ""},
		},
	}}
	if tool.Name() != "AskUser" {
		t.Fatalf("name = %q", tool.Name())
	}

	def := tool.Definition()
	if def.Name != "AskUser" {
		t.Fatalf("def.Name = %q", def.Name)
	}
	if !strings.Contains(def.Description, "спросить") {
		t.Fatalf("описание не по-русски: %q", def.Description)
	}
	props, _ := def.Parameters["properties"].(map[string]any)
	if _, ok := props["questions"]; !ok {
		t.Fatalf("схема не содержит questions: %v", def.Parameters)
	}

	out, err := tool.Execute(map[string]any{
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
				"id":          "q2",
				"text":        "Какие фичи?",
				"kind":        "multi",
				"allow_custom": false,
				"options": []any{
					map[string]any{"id": "a", "label": "А"},
					map[string]any{"id": "b", "label": "Б"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res struct {
		Status  string                   `json:"status"`
		Answers []map[string]any         `json:"answers"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("разбор результата: %v", err)
	}
	if res.Status != "answered" || len(res.Answers) != 2 {
		t.Fatalf("result = %s", out)
	}
}

// TestAskUserToolInvalidArguments — невалидные аргументы не роняют Execute:
// возвращается status=error, агентский цикл может продолжаться.
func TestAskUserToolInvalidArguments(t *testing.T) {
	tool := &askTool{b: &fakeAskBackend{}}

	for name, args := range map[string]map[string]any{
		"пусто":   {},
		"нет текста": {"questions": []any{map[string]any{"id": "q", "kind": "single", "options": []any{map[string]any{"id": "a", "label": "А"}}}}},
		"нет опций": {"questions": []any{map[string]any{"id": "q", "text": "?", "kind": "single", "options": []any{}}}},
		"дубль id":  {"questions": []any{
			map[string]any{"id": "q", "text": "?", "kind": "single", "options": []any{map[string]any{"id": "a", "label": "А"}}},
			map[string]any{"id": "q", "text": "!", "kind": "single", "options": []any{map[string]any{"id": "b", "label": "Б"}}},
		}},
	} {
		out, err := tool.Execute(args)
		if err != nil {
			t.Fatalf("%s: Execute вернул ошибку: %v", name, err)
		}
		var res struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("%s: разбор: %v", name, err)
		}
		if res.Status != "error" {
			t.Errorf("%s: status = %q, want error; out=%s", name, res.Status, out)
		}
	}
}

// TestAskUserDefaultKindAndCustom — kind по умолчанию single, allow_custom
// по умолчанию true.
func TestAskUserDefaultKindAndCustom(t *testing.T) {
	tool := &askTool{b: &fakeAskBackend{}}
	_, _ = tool.Execute(map[string]any{
		"questions": []any{
			map[string]any{"id": "q", "text": "?", "options": []any{map[string]any{"id": "a", "label": "А"}}},
		},
	})
	bh := tool.b.(*fakeAskBackend)
	q := bh.gotQuestions[0]
	if q.Kind != chat.AskSingle {
		t.Fatalf("kind = %q, want single", q.Kind)
	}
	if !q.AllowCustom {
		t.Fatalf("allow_custom должен быть true по умолчанию")
	}
	if len(q.Options) != 1 || q.Options[0].Label != "А" {
		t.Fatalf("options = %+v", q.Options)
	}
}

// TestSessionAskUserBlocksUntilAnswered — полный цикл на реальной сессии:
// AskUser публикует вопрос в чат (role=ask) и блокируется; ответ на каждый
// вопрос через Session.AnswerAsk; по последнему ответу инструмент разблоки-
// ровывается и получает ответы в порядке вопросов.
func TestSessionAskUserBlocksUntilAnswered(t *testing.T) {
	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-ask-cycle")
	sess, _, err := srv.getOrCreate("proj-ask-cycle")
	if err != nil {
		t.Fatal(err)
	}

	questions := []chat.AskQuestion{
		{
			ID:   "q1",
			Text: "Какой язык?",
			Kind: chat.AskSingle,
			Options: []chat.AskOption{
				{ID: "go", Label: "Go", Recommended: true},
				{ID: "rust", Label: "Rust"},
			},
		},
		{
			ID:          "q2",
			Text:        "Какие фичи?",
			Kind:        chat.AskMulti,
			AllowCustom: true,
			Options: []chat.AskOption{
				{ID: "a", Label: "Авторизация"},
				{ID: "b", Label: "Логи"},
			},
		},
	}

	type out struct {
		ans []chat.AskAnswer
		err error
	}
	done := make(chan out, 1)
	ctx := context.Background()
	go func() {
		ans, err := sess.AskUser(ctx, questions)
		done <- out{ans, err}
	}()

	waited := waitChatRole(t, sess, chat.RoleAsk, 3*time.Second)
	if waited.Ask == nil || len(waited.Ask.Questions) != 2 {
		t.Fatalf("ask-сообщение = %+v", waited.Ask)
	}
	askID := waited.Ask.ID
	if askID == "" {
		t.Fatal("ask ID пустой")
	}

	// Вопрос ещё не отвечен — блокирующая горутина не завершилась.
	select {
	case o := <-done:
		t.Fatalf("AskUser завершился до ответа: %+v", o)
	default:
	}

	// Ответ на 1-й вопрос: 1/2 — инструмент продолжает ждать.
	answered, total, err := sess.AnswerAsk(askID, "q1", []string{"go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if answered != 1 || total != 2 {
		t.Fatalf("answered/total = %d/%d", answered, total)
	}
	select {
	case o := <-done:
		t.Fatalf("AskUser завершился после 1/2: %+v", o)
	default:
	}

	// Ответ на 2-й вопрос: 2/2 — AskUser разблокируется.
	answered, total, err = sess.AnswerAsk(askID, "q2", []string{"a", "b"}, "свой текст")
	if err != nil {
		t.Fatal(err)
	}
	if answered != 2 || total != 2 {
		t.Fatalf("answered/total = %d/%d", answered, total)
	}

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("AskUser: %v", o.err)
		}
		if len(o.ans) != 2 || o.ans[0].QuestionID != "q1" || o.ans[1].QuestionID != "q2" {
			t.Fatalf("ответы в неверном порядке: %+v", o.ans)
		}
		if len(o.ans[0].Selected) != 1 || o.ans[0].Selected[0] != "go" {
			t.Fatalf("ans[0] = %+v", o.ans[0])
		}
		if o.ans[1].Custom != "свой текст" || len(o.ans[1].Selected) != 2 {
			t.Fatalf("ans[1] = %+v", o.ans[1])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AskUser не разблокировался после всех ответов")
	}
}

// TestSessionAskGuardErrors — охрана одного активного вопроса и валидация
// ответа: повторный AskUser, неверный askID, неизвестный вариант.
func TestSessionAskGuardErrors(t *testing.T) {
	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-ask-guard")
	sess, _, err := srv.getOrCreate("proj-ask-guard")
	if err != nil {
		t.Fatal(err)
	}

	qs := []chat.AskQuestion{
		{ID: "q", Text: "?", Kind: chat.AskSingle, Options: []chat.AskOption{{ID: "a", Label: "А"}}},
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_, err := sess.AskUser(ctx, qs)
		done <- err
	}()

	m := waitChatRole(t, sess, chat.RoleAsk, 3*time.Second)
	askID := m.Ask.ID

	// Повторный AskUser при активном — ошибка.
	if _, err := sess.AskUser(ctx, qs); err == nil {
		t.Fatal("повторный AskUser должен падать")
	}

	// Ответ несуществующему askID.
	if _, _, err := sess.AnswerAsk("ask-not-exist", "q", nil, ""); err == nil {
		t.Fatal("AnswerAsk с неверным askID должен падать")
	}
	// Несуществующий вариант.
	if _, _, err := sess.AnswerAsk(askID, "q", []string{"nope"}, ""); err == nil {
		t.Fatal("AnswerAsk с несуществующим вариантом должен падать")
	}
	// Несуществующий вопрос в пачке.
	if _, _, err := sess.AnswerAsk(askID, "q-wrong", []string{"a"}, ""); err == nil {
		t.Fatal("AnswerAsk с несуществующим вопросом должен падать")
	}

	// Полный ответ — разблокировка.
	if _, _, err := sess.AnswerAsk(askID, "q", []string{"a"}, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AskUser: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AskUser не разблокировался")
	}
	// Ответ на закрытый вопрос.
	if _, _, err := sess.AnswerAsk(askID, "q", []string{"a"}, ""); err == nil {
		t.Fatal("AnswerAsk на закрытый вопрос должен падать")
	}
}

// TestAskAnswerViaREST — REST-маршрут POST /api/projects/{id}/ask/{askID}/answer
// принимает ответ пользователя и возвращает {answered, total}.
func TestAskAnswerViaREST(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-ask-rest")
	sess, _, err := srv.getOrCreate("proj-ask-rest")
	if err != nil {
		t.Fatal(err)
	}

	qs := []chat.AskQuestion{
		{ID: "q1", Text: "Язык?", Kind: chat.AskSingle, Options: []chat.AskOption{{ID: "go", Label: "Go", Recommended: true}}},
		{ID: "q2", Text: "Фичи?", Kind: chat.AskMulti, Options: []chat.AskOption{{ID: "a", Label: "А"}}},
	}
	done := make(chan error, 1)
	ctx := context.Background()
	go func() {
		_, err := sess.AskUser(ctx, qs)
		done <- err
	}()

	m := waitChatRole(t, sess, chat.RoleAsk, 3*time.Second)
	askID := m.Ask.ID

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST",
			"/api/projects/proj-ask-rest/ask/"+askID+"/answer",
			strings.NewReader(body))
		handler.ServeHTTP(rec, req)
		return rec
	}

	rec := post(`{"question_id":"q1","selected":["go"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ответ q1: code %d body %s", rec.Code, rec.Body.String())
	}
	var r1 struct {
		OK       bool `json:"ok"`
		Answered int  `json:"answered"`
		Total    int  `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r1); err != nil {
		t.Fatal(err)
	}
	if !r1.OK || r1.Answered != 1 || r1.Total != 2 {
		t.Fatalf("r1 = %+v", r1)
	}

	rec = post(`{"question_id":"q2","selected":["a"],"custom":"доп"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ответ q2: code %d body %s", rec.Code, rec.Body.String())
	}
	var r2 struct {
		OK       bool `json:"ok"`
		Answered int  `json:"answered"`
		Total    int  `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r2); err != nil {
		t.Fatal(err)
	}
	if !r2.OK || r2.Answered != 2 || r2.Total != 2 {
		t.Fatalf("r2 = %+v", r2)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AskUser: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AskUser не разблокировался через REST")
	}

	// После закрытия — 409.
	rec = post(`{"question_id":"q1","selected":["go"]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("повторный ответ: code %d, want 409", rec.Code)
	}

	// История: пачка вопросов + статусная строка с итогом.
	hist, err := sess.chat.History(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var gotStatus bool
	for _, hmm := range hist {
		if hmm.Role == chat.RoleStatus && strings.Contains(hmm.Content, "Получены ответы") {
			gotStatus = true
		}
	}
	if !gotStatus {
		t.Fatal("в истории нет итоговой статус-строки с ответами")
	}
}

// TestChatAssistantAskUserRoundTrip — полный цикл runChatAssistant: модель
// вызывает AskUser (инструмент блокирует Generate), пользователь отвечает по
// REST, инструмент разблокируется и возвращает ответы, модель завершает текст.
func TestChatAssistantAskUserRoundTrip(t *testing.T) {
	srv, handler, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-ask-rt")
	sess, _, err := srv.getOrCreate("proj-ask-rt")
	if err != nil {
		t.Fatal(err)
	}

	rep := &runner.ModelReply{
		ToolCalls: []tools.ToolCall{{
			Name: "AskUser",
			Arguments: `{"questions":[{"id":"q1","text":"Какой язык для порта?","kind":"single","options":[{"id":"go","label":"Go","recommended":true},{"id":"rust","label":"Rust"}]}]}`,
		}},
		FinishReason: "tool_calls",
	}
	prov := &scriptedGenerateProvider{chat: &scriptedChatProvider{replies: []*runner.ModelReply{
		rep,
		{Content: "Отлично, портируем на Go.", FinishReason: "stop"},
	}}}
	sess.runChatAssistant(context.Background(), "порт на Go или Rust?", prov)

	m := waitChatRole(t, sess, chat.RoleAsk, 3*time.Second)
	askID := m.Ask.ID

	// Пользователь отвечает через REST маршрут.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST",
		"/api/projects/proj-ask-rt/ask/"+askID+"/answer",
		strings.NewReader(`{"question_id":"q1","selected":["go"]}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("REST ответ: code %d body %s", rec.Code, rec.Body.String())
	}

	// После разблокировки модель завершает цикл и ассистент пишет финальный текст.
	final := waitChatAssistant(t, sess, 3*time.Second)
	if !strings.Contains(final.Content, "Go") {
		t.Fatalf("финальный ответ = %q", final.Content)
	}
}