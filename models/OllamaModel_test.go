package models

import (
	"ai/runner"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
)

// Отмена контекста (остановка оркестрации пользователем, graceful shutdown,
// таймаут шага) должна оставаться различимой через errors.Is: server/session
// по этому отличает «оркестрация остановлена» от «прервана ошибкой» и не пугает
// пользователя статусом error при обычной остановке.
func TestChatStreamCanceledContextIsWrappable(t *testing.T) {
	u, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	p := &OllamaProvider{client: api.NewClient(u, http.DefaultClient), model: "test", settings: ModelSettings{InputTokens: 1024}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = p.ChatStream(ctx, &testAgent{}, []runner.Message{{Role: "user", Content: "привет"}}, nil)
	if err == nil {
		t.Fatal("ожидалась ошибка отменённого контекста")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ошибка должна разворачиваться в context.Canceled, got %v", err)
	}
	if !strings.Contains(err.Error(), "Ошибка выполнения Chat") {
		t.Fatalf("контекст сообщения провайдера потерян: %v", err)
	}
}

// fakeChatServer имитирует /api/chat Ollama: счётчик запросов и настраиваемые
// обработчики ошибки/успеха. Успешный ответ отдаёт один NDJSON-фрагмент с
// tool-call (BoardCreateTask) и фактическим потреблением токенов — как реальный
// стрим llama-server.
func fakeChatServer(t *testing.T, fail func(n int) bool) (*httptest.Server, *int32) {
	t.Helper()
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		n := atomic.AddInt32(&attempts, 1)
		if fail(int(n)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			// Точная строка из накопителя аргументов llama-server
			// (llm/llama_server.go): модель обрезала JSON tool-call.
			fmt.Fprintf(w, `{"error":"llama-server returned invalid tool call arguments for \"BoardCreateTask\": unexpected end of JSON input"}`)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"model":"test","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"name":"BoardCreateTask","arguments":{"task_id":"CALC-01-1","epic_id":"CALC-01","title":"Тест","description":"Описание","assigned_role":"DevOps Engineer"}}}]},"done":true,"done_reason":"stop","eval_count":42,"prompt_eval_count":100}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &attempts
}

// «Обрубленный» tool-call: llama-server не может распарсить аргументы вызова
// (модель оборвала JSON на полуслове) и возвращает ошибку. Это стохастическая
// ошибка генерации — авто-ретрай повторяет запрос с той же историей, и второй
// проход должен вернуть корректный вызов. Оркестрация не падает на раунде.
func TestChatStreamRetriesInvalidToolCallArgs(t *testing.T) {
	t.Setenv("OLLAMA_TOOL_RETRIES", "2")
	t.Setenv("OLLAMA_TOOL_RETRY_DELAY", "0")

	srv, attempts := fakeChatServer(t, func(n int) bool { return n == 1 })
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	p := &OllamaProvider{client: api.NewClient(u, http.DefaultClient), model: "test", settings: ModelSettings{InputTokens: 1024}}

	rep, err := p.ChatStream(context.Background(), &testAgent{}, []runner.Message{{Role: "user", Content: "привет"}}, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if got := atomic.LoadInt32(attempts); got != 2 {
		t.Fatalf("ожидали 2 запроса (неудача + повтор), got %d", got)
	}
	if len(rep.ToolCalls) != 1 || rep.ToolCalls[0].Name != "BoardCreateTask" {
		t.Fatalf("ожидали tool-call BoardCreateTask, got %+v", rep.ToolCalls)
	}
	if rep.Usage == nil || rep.Usage.OutputTokens != 42 {
		t.Fatalf("ожидали usage с eval_count=42, got %+v", rep.Usage)
	}
}

// Несколько попыток подряд могут обрываться — ретрай должен выдержать их в
// пределах OLLAMA_TOOL_RETRIES, прежде чем отдать ошибку.
func TestChatStreamRetriesExhausted(t *testing.T) {
	t.Setenv("OLLAMA_TOOL_RETRIES", "2")
	t.Setenv("OLLAMA_TOOL_RETRY_DELAY", "0")

	srv, attempts := fakeChatServer(t, func(n int) bool { return true })
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	p := &OllamaProvider{client: api.NewClient(u, http.DefaultClient), model: "test", settings: ModelSettings{InputTokens: 1024}}

	_, err = p.ChatStream(context.Background(), &testAgent{}, []runner.Message{{Role: "user", Content: "привет"}}, nil)
	if err == nil {
		t.Fatal("ожидалась ошибка при исчерпании повторов")
	}
	if !strings.Contains(err.Error(), "invalid tool call arguments") {
		t.Fatalf("потеряна причина ошибки: %v", err)
	}
	if !strings.Contains(err.Error(), "Ошибка выполнения Chat") {
		t.Fatalf("контекст сообщения провайдера потерян: %v", err)
	}
	var se api.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("ошибка должна разворачиваться в api.StatusError, got %T: %v", err, err)
	}
	if got := atomic.LoadInt32(attempts); got != 3 {
		t.Fatalf("ожидали 3 запроса (1 + 2 повтора), got %d", got)
	}
}

// Неретраируемые ошибки (контекст, сеть, генерация) не повторяются — ретрай
// затрагивает только «обрубленные» tool-call, а не маскирует настоящие поломки.
func TestChatStreamNoRetryOnNonRetryableError(t *testing.T) {
	t.Setenv("OLLAMA_TOOL_RETRIES", "5")
	t.Setenv("OLLAMA_TOOL_RETRY_DELAY", "0")

	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"error reading llama-server chat response"}`)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	p := &OllamaProvider{client: api.NewClient(u, http.DefaultClient), model: "test", settings: ModelSettings{InputTokens: 1024}}

	_, err = p.ChatStream(context.Background(), &testAgent{}, []runner.Message{{Role: "user", Content: "привет"}}, nil)
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("неретраируемая ошибка не должна повторяться, got %d запросов", got)
	}
}

func TestStreamIdleIntervalParsing(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"0", 0},
		{"off", 0},
		{"false", 0},
		{"90s", 90 * time.Second},
		{"2m", 2 * time.Minute},
		{"1", time.Second},
		{"120", 2 * time.Minute},
		{"abc", streamIdleDefault},
	}
	for _, tc := range cases {
		t.Setenv("OLLAMA_STREAM_IDLE", tc.env)
		if got := streamIdleInterval(); got != tc.want {
			t.Errorf("OLLAMA_STREAM_IDLE=%q: got %v, want %v", tc.env, got, tc.want)
		}
	}
	t.Setenv("OLLAMA_STREAM_IDLE", "")
	if got := streamIdleInterval(); got != streamIdleDefault {
		t.Errorf("по умолчанию: got %v, want %v", got, streamIdleDefault)
	}
}

// silentChatServer имитирует мёртвый бэкенд: отдаёт один кадр, затем держит
// соединение открытым без новых фрагментов и без EOF — ровно то состояние,
// в которое встаёт ollama, когда llama-server умер на середине генерации.
func silentChatServer(t *testing.T, attempts *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(attempts, 1)
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"model":"test","message":{"role":"assistant","content":"частично"}}`)
		if fl != nil {
			fl.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Замолчавший стрим (бэкенд умер: клиент Ollama держит соединение без кадров)
// должен прерываться идл-вотчдогом, а не висеть вечно: без таймаута SDK
// читает стрим блокирующим Scan() через http.DefaultClient (Timeout=0).
func TestChatStreamIdleWatchdogAbortsSilentBackend(t *testing.T) {
	t.Setenv("OLLAMA_STREAM_IDLE", "1")
	var attempts int32
	srv := silentChatServer(t, &attempts)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	p := &OllamaProvider{client: api.NewClient(u, http.DefaultClient), model: "test", settings: ModelSettings{InputTokens: 1024}}

	_, err = p.ChatStream(context.Background(), &testAgent{}, []runner.Message{{Role: "user", Content: "привет"}}, nil)
	if err == nil {
		t.Fatal("ожидалась ошибка замолчавшего стрима")
	}
	if !strings.Contains(err.Error(), "замолчал") {
		t.Fatalf("ожидали диагноз «замолчал», got: %v", err)
	}
	// Замолчавший бэкенд — не остановка пользователем: оркестрация должна
	// увидеть ошибку, а не context.Canceled.
	if errors.Is(err, context.Canceled) {
		t.Fatalf("ошибка не должна разворачиваться в context.Canceled: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("ожидали один запрос, got %d", got)
	}
}

// Живой стрим с паузами заметно меньше лимита не должен задевать вотчдог:
// кадры идут, lastFrame обновляется, генерация завершается штатно.
func TestChatStreamIdleWatchdogHealthyStream(t *testing.T) {
	t.Setenv("OLLAMA_STREAM_IDLE", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		for _, f := range []string{
			`{"model":"test","message":{"role":"assistant","content":"при"}}`,
			`{"model":"test","message":{"role":"assistant","content":"в"}}`,
			`{"model":"test","message":{"role":"assistant","content":"ет"}}`,
			`{"model":"test","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","eval_count":3,"prompt_eval_count":10}`,
		} {
			fmt.Fprintln(w, f)
			fl.Flush()
			time.Sleep(200 * time.Millisecond)
		}
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	p := &OllamaProvider{client: api.NewClient(u, http.DefaultClient), model: "test", settings: ModelSettings{InputTokens: 1024}}

	rep, err := p.ChatStream(context.Background(), &testAgent{}, []runner.Message{{Role: "user", Content: "привет"}}, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if rep.Content != "привет" {
		t.Fatalf("ожидали полный текст «привет», got %q", rep.Content)
	}
	if rep.Usage == nil || rep.Usage.OutputTokens != 3 {
		t.Fatalf("ожидали usage с eval_count=3, got %+v", rep.Usage)
	}
}
