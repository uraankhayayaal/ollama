package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai/agents"
	"ai/models"
	"ai/runner"
)

// analysisFixture — проект с логом, в котором есть и ошибка, и шум.
func analysisFixture(t *testing.T, proj string, body string) *Session {
	t.Helper()
	sess, srv := logTestSession(t, proj)
	global := t.TempDir()
	t.Setenv("LOG_DIR", global)
	writeLogFile(t, global, proj+".log", body)
	_ = srv
	return sess
}

const analysisLogBody = "2026-01-01 10:00:00.000 INFO   оркестрация начата\n" +
	"2026-01-01 10:00:01.000 ERROR server: не удалось подключиться к БД\n" +
	"2026-01-01 10:00:02.000 INFO   шаг 2\n" +
	"2026-01-01 10:00:03.000 WARN   повторяющиеся таймауты апстрима\n"

// Разбор логов — это запрос к ассистенту: дайджест должен попасть в промпт,
// а не остаться в серверном коде.
func TestLogAnalysisDigestGoesToPrompt(t *testing.T) {
	sess := analysisFixture(t, "an-prompt", analysisLogBody)
	a, err := sess.buildLogAnalysis(logAnalysisReq{File: "an-prompt.log"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Digest.Findings) != 2 {
		t.Fatalf("находок=%d, want 2: %+v", len(a.Digest.Findings), a.Digest.Findings)
	}
	if a.Digest.Findings[0].Level != "ERROR" {
		t.Errorf("первой находкой должен быть ERROR, got %q", a.Digest.Findings[0].Level)
	}

	p := sess.chatPromptFor(chatAsk{question: "разбери лог", logDigestPrompt: logDigestPrompt(a)})
	for _, want := range []string{
		"РАЗБОР ЛОГА ПРОЕКТА", "an-prompt.log", "не удалось подключиться",
		"таймаут", "ReadProjectLogs", "первопричина",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("в промпте нет %q", want)
		}
	}
	// INFO в разбор не попадает.
	if strings.Contains(p, "оркестрация начата") {
		t.Errorf("в промпте есть INFO-строка: %s", p)
	}
}

// Контракт ответа задан в промпте явно: без него модель пересказывает лог.
func TestLogAnalysisPromptStatesResponseContract(t *testing.T) {
	sess := analysisFixture(t, "an-contract", analysisLogBody)
	a, err := sess.buildLogAnalysis(logAnalysisReq{File: "an-contract.log"})
	if err != nil {
		t.Fatal(err)
	}
	block := logDigestPrompt(a)
	for _, want := range []string{"ЦИТАТА", "гипотез", "шумом", "следующие шаги", "критичного нет"} {
		if !strings.Contains(block, want) {
			t.Errorf("в контракте ответа нет %q:\n%s", want, block)
		}
	}
}

// Фильтр уровня — «не легче»: level=ERROR не должен тащить WARN.
func TestLogAnalysisLevelFilterKeepsOnlyWorse(t *testing.T) {
	sess := analysisFixture(t, "an-level", analysisLogBody)

	a, err := sess.buildLogAnalysis(logAnalysisReq{File: "an-level.log", Level: "ERROR"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range a.Digest.Findings {
		if levelRank(f.Level) < levelRank("ERROR") {
			t.Errorf("в разбор попал %q при level=ERROR", f.Level)
		}
	}

	// С уровнем WARN приходят обе находки, с FATAL — ни одной.
	a, _ = sess.buildLogAnalysis(logAnalysisReq{File: "an-level.log", Level: "warn"})
	if len(a.Digest.Findings) != 2 {
		t.Errorf("level=warn: находок=%d, want 2", len(a.Digest.Findings))
	}
	a, _ = sess.buildLogAnalysis(logAnalysisReq{File: "an-level.log", Level: "FATAL"})
	if len(a.Digest.Findings) != 0 {
		t.Errorf("level=FATAL: находок=%d, want 0", len(a.Digest.Findings))
	}
	if !strings.Contains(a.Note, "не легче FATAL") {
		t.Errorf("условия выборки не отражены: %q", a.Note)
	}
}

// Поиск из панели логов ограничивает выборку заранее, до группировки.
func TestLogAnalysisQueryFilter(t *testing.T) {
	sess := analysisFixture(t, "an-query", analysisLogBody)
	a, err := sess.buildLogAnalysis(logAnalysisReq{File: "an-query.log", Query: "апстрим"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Digest.Findings) != 1 {
		t.Fatalf("находок=%d, want 1: %+v", len(a.Digest.Findings), a.Digest.Findings)
	}
	if !strings.Contains(a.Digest.Findings[0].Template, "таймаут") {
		t.Errorf("осталась не та находка: %q", a.Digest.Findings[0].Template)
	}
	if !strings.Contains(a.Note, "апстрим") {
		t.Errorf("подстрока поиска не отражена в условиях выборки: %q", a.Note)
	}
	// Модель должна понимать, что видит подмножество.
	if !strings.Contains(logDigestPrompt(a), "Условия выборки") {
		t.Errorf("условия выборки не попали в промпт")
	}
}

// Кадры стека не теряются при фильтре по уровню: иначе находка лишается
// доказательства.
func TestLogAnalysisLevelFilterKeepsStackFrames(t *testing.T) {
	body := "2026-01-01 10:00:00.000 ERROR boom\n  at com.foo.Bar(Bar.java:42)\n2026-01-01 10:00:01.000 WARN   шум\n"
	sess := analysisFixture(t, "an-stack", body)
	a, err := sess.buildLogAnalysis(logAnalysisReq{File: "an-stack.log", Level: "ERROR"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Digest.Findings) != 1 || a.Digest.Findings[0].Frame == "" {
		t.Fatalf("кадр стека потерян: %+v", a.Digest.Findings)
	}
}

func TestLogAnalysisUnknownFile(t *testing.T) {
	sess := analysisFixture(t, "an-miss", analysisLogBody)
	if _, err := sess.buildLogAnalysis(logAnalysisReq{File: "nope.log"}); err == nil {
		t.Fatal("ожидалась ошибка для несуществующего файла")
	}
}

func TestLogAnalysisUnknownProject(t *testing.T) {
	sess := &Session{srv: newLogTestServer(t), project: "нет-такого"}
	if _, err := sess.buildLogAnalysis(logAnalysisReq{}); err == nil {
		t.Fatal("ожидалась ошибка для неизвестного проекта")
	}
}

// --- HTTP-слой ---

// newLogTestServer — сервер без настроенного LLM-провайдера: путь разбора
// должен работать и отдавать 400/«проблем нет» без модели.
func newLogTestServer(t *testing.T) *Server {
	t.Helper()
	srv, _, _ := newTestServer(t)
	return srv
}

// countingProvider считает вызовы Generate: им доказываем, что пустой разбор и
// cooldown НЕ зовут модель (иначе на «всё хорошо» тратятся токены, а на
// повторном клике — целый ход ассистента).
type countingProvider struct {
	calls atomic.Int64
}

func newCountingProvider() *countingProvider { return &countingProvider{} }

func (p *countingProvider) Generate(context.Context, agents.Agent) (*runner.AgentResponse, error) {
	p.calls.Add(1)
	return &runner.AgentResponse{Content: "разбор: главное — ошибка подключения к БД"}, nil
}

func setProviderForTest(srv *Server, p models.LLMProvider) {
	srv.prov.mu.Lock()
	defer srv.prov.mu.Unlock()
	srv.prov.cached = &models.Resolved{Provider: p, Name: "test"}
	srv.prov.done = true
	srv.prov.err = nil
}

// postLogAnalysis шлёт POST /api/projects/{id}/chat с телом body и ждёт
// завершения хода ассистента.
func postLogAnalysis(t *testing.T, sess *Session, body string) (string, int) {
	t.Helper()
	return postChatBody(t, sess, body)
}

func postChatBody(t *testing.T, sess *Session, body string) (string, int) {
	t.Helper()
	srv := sess.srv
	req := httptest.NewRequest("POST", "/api/projects/"+sess.project+"/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	// Ход ассистента асинхронный — дожидаемся ответа в чате.
	sess.wg.Wait()
	return rec.Body.String(), rec.Code
}

func readChatHistory(t *testing.T, sess *Session) string {
	t.Helper()
	hist, err := sess.chat.History(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, m := range hist {
		b.WriteString(string(m.Role) + ": " + m.Content + "\n")
	}
	return b.String()
}

// Главный сценарий: кнопка «Разобрать» → ответ в чате. Проверяем, что в чат
// уехали служебная строка и ответ ассистента, а НЕ выдуманная реплика
// пользователя.
func TestChatLogAnalysisAnswersInChat(t *testing.T) {
	sess := analysisFixture(t, "an-http", analysisLogBody)
	setProviderForTest(sess.srv, newCountingProvider())

	out, code := postLogAnalysis(t, sess, `{"log_analysis":{"file":"an-http.log"}}`)
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, out)
	}

	hist := readChatHistory(t, sess)
	if !strings.Contains(hist, "Разбор логов") {
		t.Errorf("нет служебной строки о разборе:\n%s", hist)
	}
	if !strings.Contains(hist, "ошибка подключения") {
		t.Errorf("нет ответа ассистента:\n%s", hist)
	}
	if strings.Contains(hist, "user:") {
		t.Errorf("в чат записана выдуманная реплика пользователя:\n%s", hist)
	}
}

func TestChatLogAnalysisRejectsGarbage(t *testing.T) {
	sess := analysisFixture(t, "an-bad", analysisLogBody)
	_, code := postLogAnalysis(t, sess, `{"message":"   "}`)
	if code != http.StatusBadRequest {
		t.Fatalf("code=%d, want 400", code)
	}
}

func TestChatLogAnalysisUnknownFileIs400(t *testing.T) {
	sess := analysisFixture(t, "an-http-404", analysisLogBody)
	_, code := postLogAnalysis(t, sess, `{"log_analysis":{"file":"nope.log"}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("code=%d, want 400", code)
	}
	if !strings.Contains(readChatHistory(t, sess), "не удался") {
		t.Errorf("в чат не сообщили об ошибке:\n%s", readChatHistory(t, sess))
	}
}

// Пустой лог: ответ детерминированный, модель НЕ зовётся (иначе на пустом
// дайджесте она склонна выдумать проблему и потратить токены).
func TestChatLogAnalysisEmptyLogSkipsLLM(t *testing.T) {
	sess := analysisFixture(t, "an-empty", "2026-01-01 10:00:00.000 INFO   всё хорошо\n2026-01-01 10:00:01.000 INFO   и тут хорошо\n")
	prov := newCountingProvider()
	setProviderForTest(sess.srv, prov)

	body, code := postLogAnalysis(t, sess, `{"log_analysis":{"file":"an-empty.log"}}`)
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	if prov.calls.Load() != 0 {
		t.Fatalf("модель вызвана %d раз на пустом логе", prov.calls.Load())
	}
	if !strings.Contains(body, `"empty":true`) {
		t.Errorf("ответ не помечен как пустой разбор: %s", body)
	}
	hist := readChatHistory(t, sess)
	if !strings.Contains(hist, "критичных проблем не найдено") {
		t.Errorf("нет ответа «проблем нет»:\n%s", hist)
	}
}

// Двойной клик по кнопке не должен дважды звать модель.
func TestChatLogAnalysisCooldownSkipsSecondRun(t *testing.T) {
	sess := analysisFixture(t, "an-cool", analysisLogBody)
	prov := newCountingProvider()
	setProviderForTest(sess.srv, prov)

	if _, code := postLogAnalysis(t, sess, `{"log_analysis":{"file":"an-cool.log"}}`); code != http.StatusOK {
		t.Fatalf("code=%d", code)
	}
	before := prov.calls.Load()
	if before != 1 {
		t.Fatalf("первый разбор должен был позвать модель, вызовов=%d", before)
	}
	body, code := postLogAnalysis(t, sess, `{"log_analysis":{"file":"an-cool.log"}}`)
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	if !strings.Contains(body, `"cached":true`) {
		t.Errorf("второй разбор не отсечён: %s", body)
	}
	if prov.calls.Load() != before {
		t.Errorf("модель вызвана повторно: %d → %d", before, prov.calls.Load())
	}
	if !strings.Contains(readChatHistory(t, sess), "не изменился") {
		t.Errorf("нет пояснения про cooldown:\n%s", readChatHistory(t, sess))
	}
}

// Изменился лог — cooldown снимается: ключ включает размер/время находок.
func TestLogAnalysisKeyChangesWithLog(t *testing.T) {
	sess := analysisFixture(t, "an-key", analysisLogBody)
	a1, _ := sess.buildLogAnalysis(logAnalysisReq{File: "an-key.log"})
	// Дописываем строку с другой ошибкой — ключ обязан измениться.
	writeLogFile(t, sess.srv.logsDir(), "an-key.log", analysisLogBody+
		"2026-01-01 10:00:09.000 FATAL  сервер упал\n")
	a2, _ := sess.buildLogAnalysis(logAnalysisReq{File: "an-key.log"})
	if a1.key() == a2.key() {
		t.Fatalf("ключ не изменился после правки лога: %q", a1.key())
	}
	if sess.markAnalyzed(a1.key()) {
		t.Fatalf("первый разбор помечен как недавний")
	}
	if !sess.markAnalyzed(a1.key()) {
		t.Errorf("повтор того же ключа не отсечён")
	}
	if sess.markAnalyzed(a2.key()) {
		t.Errorf("новый ключ отсечён как недавний")
	}
}
