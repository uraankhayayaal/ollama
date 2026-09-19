package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetTokensStartsAtZero проверяет REST-эндпоинт счётчика токенов:
// новый проект возвращает нули.
func TestGetTokensStartsAtZero(t *testing.T) {
	_, handler, _ := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/tok-proj/tokens", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET tokens: %d, body: %s", rec.Code, rec.Body.String())
	}
	var ev tokenEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Input != 0 || ev.Output != 0 {
		t.Fatalf("tokens = %d/%d, want 0/0", ev.Input, ev.Output)
	}
}

// TestTokensAccumulatePerProject проверяет накопление токенов через сессию
// (addTokens — путь WS-событий) и чтение REST-эндпоинтом: суммы хранятся в
// Redis за время жизни проекта и не смешиваются между проектами.
func TestTokensAccumulatePerProject(t *testing.T) {
	srv, handler, _ := newTestServer(t)

	sess, _, err := srv.getOrCreate("tok-a")
	if err != nil {
		t.Fatalf("getOrCreate tok-a: %v", err)
	}
	_, _, err = srv.getOrCreate("tok-b")
	if err != nil {
		t.Fatalf("getOrCreate tok-b: %v", err)
	}

	sess.addTokens(100, 40)
	sess.addTokens(50, 60)

	// tok-a накопил 150/100.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects/tok-a/tokens", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET tokens tok-a: %d", rec.Code)
	}
	var ev tokenEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Input != 150 || ev.Output != 100 {
		t.Fatalf("tok-a tokens = %d/%d, want 150/100", ev.Input, ev.Output)
	}

	// tok-b остался пустым.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/projects/tok-b/tokens", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET tokens tok-b: %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Input != 0 || ev.Output != 0 {
		t.Fatalf("tok-b tokens = %d/%d, want 0/0", ev.Input, ev.Output)
	}
}
