// Hermetic-тесты WebSearch: формат ответа, разбор HTML-выдачи DuckDuckGo
// (заголовки/URL/сниппеты, редирект uddg), degrade при сетевой ошибке и
// не-200, лимиты выдачи. Сеть не используется: запросы идут на fake-сервер
// (httptest), клиент и endpoint инжектятся в инструмент.

package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ddgPage — минимальная HTML-выдача DuckDuckGo с двумя результатами.
func ddgPage() string {
	return `<html><body>
<div class="result">
  <div class="result__body">
    <h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Frust&rut=abc">Порт на Rust: обзор языка</a></h2>
    <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Frust">Язык Rust &mdash; системный клон C без сборщика мусора.</a>
  </div>
</div>
<div class="result">
  <div class="result__body">
    <h2 class="result__title"><a rel="nofollow" class="result__a" href="https://news.example.com/2026">Новости Rust 2026</a></h2>
    <a class="result__snippet" href="https://news.example.com/2026">Вышла новая версия компилятора.</a>
  </div>
</div>
</body></html>`
}

// emptyDDGPage — выдача без результатов (поиск ничего не нашёл).
func emptyDDGPage() string {
	return `<html><body><div class="no-results">ничего не найдено</div></body></html>`
}

// newWebSearchTestTool поднимает fake-сервер с заданными ответом и статусом и
// возвращает инструмент, настроенный на него (endpoint + client).
func newWebSearchTestTool(t *testing.T, status int, body string) (*webSearchTool, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			http.Error(w, "missing q", http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	tool := &webSearchTool{client: srv.Client(), endpoint: srv.URL + "/html/"}
	return tool, srv
}

// setEnvLimit восстанавливает WEB_SEARCH_MAX_RESULTS после теста.
func setEnvLimit(t *testing.T, v string) {
	t.Helper()
	t.Setenv("WEB_SEARCH_MAX_RESULTS", v)
}

// Успешный поиск: {"status":"success","query":…,"results":[{title,url,snippet}]}.
// URL редиректа DDG (uddg) разворачивается в реальный адрес, HTML-сущности в
// заголовке/сниппете декодируются.
func TestWebSearchSuccessParsesDDG(t *testing.T) {
	tool, _ := newWebSearchTestTool(t, http.StatusOK, ddgPage())

	out, err := tool.Execute(map[string]any{"query": "порт на Rust"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
		t.Fatalf("не JSON: %s", out)
	}
	if m["status"] != "success" {
		t.Fatalf("status: got %v, want success:\n%s", m["status"], out)
	}
	if m["query"] != "порт на Rust" {
		t.Fatalf("query: got %v", m["query"])
	}
	results, ok := m["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results: got %#v", m["results"])
	}
	r0, _ := results[0].(map[string]any)
	if r0["title"] != "Порт на Rust: обзор языка" {
		t.Fatalf("title[0]: %#v", r0["title"])
	}
	// Редирект uddg декодируется: //duckduckgo.com/l/?uddg=https%3A%2F%2F…→ https://example.com/rust
	if r0["url"] != "https://example.com/rust" {
		t.Fatalf("url[0]: got %q, want https://example.com/rust", r0["url"])
	}
	if !strings.Contains(r0["snippet"].(string), "системный клон C") {
		t.Fatalf("snippet[0]: %#v", r0["snippet"])
	}
	// Прямая ссылка (без редиректа) остаётся как есть.
	r1, _ := results[1].(map[string]any)
	if r1["url"] != "https://news.example.com/2026" {
		t.Fatalf("url[1]: %#v", r1["url"])
	}
}

// Пустой query — error без обращения к сети.
func TestWebSearchEmptyQuery(t *testing.T) {
	tool, srv := newWebSearchTestTool(t, http.StatusOK, ddgPage())
	_ = srv

	out, err := tool.Execute(map[string]any{"query": "   "})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["status"] != "error" {
		t.Fatalf("должен быть error, got %s", out)
	}
	if msg, _ := m["message"].(string); !strings.Contains(msg, "query") {
		t.Fatalf("ошибка должна упоминать query: %s", msg)
	}
}

// Нет результатов — success с пустым массивом и подсказкой переформулировать.
func TestWebSearchNoResults(t *testing.T) {
	tool, _ := newWebSearchTestTool(t, http.StatusOK, emptyDDGPage())

	out, err := tool.Execute(map[string]any{"query": "йочкхщ"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["status"] != "success" {
		t.Fatalf("должен быть success, got %s", out)
	}
	if arr, ok := m["results"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("пустые результаты должны быть []: %#v", m["results"])
	}
}

// Сетевая ошибка (таймаут/недоступность) → status degraded с маркером «данные
// не живые»; это НЕ error — агентский цикл не считает вызов провальным.
func TestWebSearchNetworkErrorDegrades(t *testing.T) {
	// Сервер закрыт сразу: клиент получит ошибку соединения.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL + "/html/"
	srv.Close()

	tool := &webSearchTool{client: srv.Client(), endpoint: addr}
	out, err := tool.Execute(map[string]any{"query": "погода в Москве"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["status"] != "degraded" {
		t.Fatalf("должен быть degraded, got %s", out)
	}
	msg, _ := m["message"].(string)
	if !strings.Contains(msg, "не живые") {
		t.Fatalf("degrade должен помечать данные как неживые: %s", msg)
	}
}

// Ненулевой HTTP-статус (403/500/429) — тоже degraded, а не падение.
func TestWebSearchNon200Degrades(t *testing.T) {
	tool, _ := newWebSearchTestTool(t, http.StatusBadGateway, "rewind")

	out, err := tool.Execute(map[string]any{"query": "новости"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["status"] != "degraded" {
		t.Fatalf("должен быть degraded, got %s", out)
	}
	if msg, _ := m["message"].(string); !strings.Contains(msg, "502") {
		t.Fatalf("сообщение должно упоминать статус ответа: %s", msg)
	}
}

// Пустое/нераспознаваемое тело выдачи — не падение: выдача пустая, success
// с пустым результатом и подсказкой (токенайзер HTML терпим к «шумовым»
// ответам, поэтому это не degrade).
func TestWebSearchEmptyBodyGivesEmptyResults(t *testing.T) {
	tool, _ := newWebSearchTestTool(t, http.StatusOK, "")

	out, err := tool.Execute(map[string]any{"query": "тест"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil || m["status"] != "success" {
		t.Fatalf("должен быть success, got %s", out)
	}
	if arr, ok := m["results"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("пустые результаты должны быть []: %#v", m["results"])
	}
}

// Лимит max_results урезает выдачу; дефолт берётся из WEB_SEARCH_MAX_RESULTS.
func TestWebSearchLimitsResults(t *testing.T) {
	tool, _ := newWebSearchTestTool(t, http.StatusOK, ddgPage())

	out, err := tool.Execute(map[string]any{"query": "rust", "max_results": 1})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
		t.Fatalf("не JSON: %s", out)
	}
	results, _ := m["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("max_results=1: got %d результатов, want 1", len(results))
	}

	setEnvLimit(t, "1")
	tool2, _ := newWebSearchTestTool(t, http.StatusOK, ddgPage())
	out, _ = tool2.Execute(map[string]any{"query": "rust"})
	json.Unmarshal(out, &m)
	results, _ = m["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("WEB_SEARCH_MAX_RESULTS=1: got %d результатов, want 1", len(results))
	}
}

// Разбор сущностей в сниппете (&mdash; → —) и нормализация переносов строк.
func TestNormalizeWebSearchText(t *testing.T) {
	if got := normalizeWebSearchText("  Язык Rust &mdash; системный,&nbsp;;\n быстрый.  "); got != "Язык Rust — системный, ; быстрый." {
		t.Fatalf("normalize: %q", got)
	}
}

// decodeDDGHref: редирект uddg разворачивается, "//…" и обычные ссылки — как есть.
func TestDecodeDDGHref(t *testing.T) {
	cases := map[string]string{
		"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fr&rut=x": "https://example.com/r",
		"//example.com/path":            "https://example.com/path",
		"https://news.example.com/2026": "https://news.example.com/2026",
		"":                              "",
	}
	for in, want := range cases {
		if got := decodeDDGHref(in); got != want {
			t.Errorf("decodeDDGHref(%q) = %q, want %q", in, got, want)
		}
	}
}

// Таймаут берётся из WEB_SEARCH_TIMEOUT.
func TestWebSearchTimeoutEnv(t *testing.T) {
	t.Setenv("WEB_SEARCH_TIMEOUT", "3s")
	if got := webSearchTimeout(); got != 3*time.Second {
		t.Fatalf("timeout: got %v, want 3s", got)
	}
	t.Setenv("WEB_SEARCH_TIMEOUT", "??")
	if got := webSearchTimeout(); got != 15*time.Second {
		t.Fatalf("битое env: got %v, want дефолт 15s", got)
	}
}
