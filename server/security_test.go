package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// newTestServerAuth — тестовый сервер с указанным паролем Web UI.
// password=="" — аутентификация выключена (как в newTestServer).
func newTestServerAuth(t *testing.T, password string) (*Server, http.Handler) {
	t.Helper()
	wsPath := filepath.Join(t.TempDir(), "workspaces.json")
	srv, err := NewServer(Config{WorkspacesPath: wsPath, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	return srv, srv.routes()
}

// login делает POST /api/login и возвращает sid, csrf из ответа.
func login(t *testing.T, handler http.Handler, password string) (sid, csrf string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/login",
		bytes.NewBufferString(`{"password":"`+password+`"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/login: %d, body: %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == authCookieName {
			sid = c.Value
		}
	}
	if sid == "" {
		t.Fatal("Set-Cookie ai_sid не найден")
	}
	var out struct {
		OK    bool   `json:"ok"`
		Login bool   `json:"login"`
		CSRF  string `json:"csrf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !out.OK || !out.Login || out.CSRF == "" {
		t.Fatalf("ответ логина = %+v", out)
	}
	return sid, out.CSRF
}

func TestAuthDisabledByDefault(t *testing.T) {
	_, handler := newTestServerAuth(t, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/auth", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/auth без пароля: %d", rec.Code)
	}
	var out struct {
		OK    bool `json:"ok"`
		Login bool `json:"login"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Login {
		t.Fatalf("защита должна быть выключена: %+v", out)
	}

	// Без пароля API открыт — 401 ни откуда не берётся.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/projects", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("защита выключена, но API гонит 401: %s", rec.Body.String())
	}
}

func TestAuthWrongPasswordRejected(t *testing.T) {
	_, handler := newTestServerAuth(t, "secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/login",
		bytes.NewBufferString(`{"password":"wrong"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d, want 401", rec.Code)
	}
	if n := len(rec.Result().Cookies()); n != 0 {
		t.Fatalf("при неудачном входе не должно быть cookie, got %d", n)
	}
}

func TestAuthGuardsAPIAndCSRFBlocksMutation(t *testing.T) {
	_, handler := newTestServerAuth(t, "secret")

	// Без сессии — 401.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET без сессии: %d, want 401", rec.Code)
	}

	sid, csrf := login(t, handler, "secret")

	// GET с сессией — авторизация пройдена (пустой список проектов).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/projects", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: sid})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET с сессией: %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	// Мутация без CSRF — 403 (даже с валидной сессией).
	dir := t.TempDir()
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/projects",
		bytes.NewBufferString(`{"path_or_git":"`+dir+`"}`))
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: sid})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST без CSRF: %d, want 403", rec.Code)
	}

	// Мутация с правильным CSRF — успешна.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/projects",
		bytes.NewBufferString(`{"path_or_git":"`+dir+`"}`))
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: sid})
	req.Header.Set(authCSRFHeader, csrf)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST с CSRF: %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestAuthStatusReturnsCSRF(t *testing.T) {
	_, handler := newTestServerAuth(t, "secret")
	sid, csrf := login(t, handler, "secret")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/auth", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: sid})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/auth: %d", rec.Code)
	}
	var out struct {
		OK    bool   `json:"ok"`
		Login bool   `json:"login"`
		CSRF  string `json:"csrf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || !out.Login || out.CSRF != csrf {
		t.Fatalf("auth status = %+v, want csrf %q", out, csrf)
	}
}

func TestAuthLoginRateLimited(t *testing.T) {
	_, handler := newTestServerAuth(t, "secret")

	// loginLim = 5 в минуту: первые 5 — проверка пароля (401), 6-я — 429.
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/login",
			bytes.NewBufferString(`{"password":"bad"}`))
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("попытка %d: %d, want 401", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/login",
		bytes.NewBufferString(`{"password":"secret"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6-я попытка: %d, want 429", rec.Code)
	}
}

func TestAuthLogoutInvalidatesSession(t *testing.T) {
	_, handler := newTestServerAuth(t, "secret")
	sid, csrf := login(t, handler, "secret")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/logout", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: sid})
	req.Header.Set(authCSRFHeader, csrf)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/logout: %d, body: %s", rec.Code, rec.Body.String())
	}

	// Сессия убита — GET /api/projects снова 401.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/projects", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: sid})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET после logout: %d, want 401", rec.Code)
	}
}

// --- rateLimiter (ratelimit.go) ---

func TestRateLimiterWindow(t *testing.T) {
	rl := newRateLimit(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.allow("ip-1") {
			t.Fatalf("запрос %d должен пройти", i+1)
		}
	}
	if rl.allow("ip-1") {
		t.Fatal("4-й запрос должен быть отклонён")
	}
	if !rl.allow("ip-2") {
		t.Fatal("чужой IP в лимит не входит")
	}
}

// TestRateLimiterWindowReset проверяет сброс окна: после истечения window
// лимит снова доступен.
func TestRateLimiterWindowReset(t *testing.T) {
	rl := newRateLimit(1, time.Minute)
	if !rl.allow("k") || rl.allow("k") {
		t.Fatal("лимит 1 в минуту не соблюдается")
	}
	rl.hits["k"].reset = time.Now().Add(-time.Second)
	if !rl.allow("k") {
		t.Fatal("после сброса окна запрос должен пройти")
	}
}