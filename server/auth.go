// Аутентификация Web UI (Ф-3): AI_WEB_PASSWORD → простой лочин.
//
// Пароль задаётся переменной окружения AI_WEB_PASSWORD (или конфигом сервера).
// Если он пуст — защита отключена (дефолт для локального use-case сервера на
// 127.0.0.1). При включённой защите:
//
//   - POST /api/login проверяет пароль и выдаёт httpOnly-сессию (cookie ai_sid,
//     SameSite=Lax, Path=/), а в JSON-ответе — per-session CSRF-токен.
//   - Все /api/* кроме /api/login и /api/auth требуют валидной сессии.
//   - Мутирующие методы (POST/PUT/DELETE/PATCH) требуют X-CSRF-Token.
//   - POST /api/logout убивает сессию и чистит cookie.
//
// Дополнительное ограничение входа — rate-limiter (см. ratelimit.go):
// 5 попыток/минуту с одного IP.

package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// authCookieName — httpOnly-сессия Web UI.
	authCookieName = "ai_sid"
	// authCSRFHeader — заголовок CSRF-токена для мутаций.
	authCSRFHeader = "X-CSRF-Token"
	// authSessionTTL — время жизни сессии (скользящее).
	authSessionTTL = 24 * time.Hour
)

// authSession — валидная сессия Web UI: CSRF-токен и срок жизни.
type authSession struct {
	csrf    string
	expires time.Time
}

// authManager хранит сессии логина и проверяет пароль. Нилевый (пароль не
// задан) означает, что аутентификация отключена.
type authManager struct {
	mu       sync.Mutex
	password string
	sessions map[string]authSession
	ttl      time.Duration
}

// newAuth создаёт менеджер, если задан пароль; иначе nil (защита выключена).
func newAuth(password string) *authManager {
	if strings.TrimSpace(password) == "" {
		return nil
	}
	return &authManager{password: password, sessions: map[string]authSession{}, ttl: authSessionTTL}
}

// enabled сообщает, включена ли аутентификация.
func (a *authManager) enabled() bool { return a != nil }

// randomID генерирует криптостойкий идентификатор.
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand не возвращает ошибок на поддерживаемых платформах.
		panic("server: crypto/rand недоступен: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// login проверяет пароль и создаёт сессию. Возвращает sessionID, CSRF-токен
// и признак успеха. Если аутентификация отключена — «успех» без сессии.
func (a *authManager) login(password string) (sid, csrf string, ok bool) {
	if !a.enabled() {
		return "", "", true
	}
	if subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) != 1 {
		return "", "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked()
	sid = randomID()
	csrf = randomID()
	a.sessions[sid] = authSession{csrf: csrf, expires: time.Now().Add(a.ttl)}
	return sid, csrf, true
}

// logout убивает сессию.
func (a *authManager) logout(sid string) {
	if !a.enabled() {
		return
	}
	a.mu.Lock()
	delete(a.sessions, sid)
	a.mu.Unlock()
}

// check валидирует сессию (скользящий TTL). Возвращает CSRF-токен и ok.
func (a *authManager) check(sid string) (csrf string, ok bool) {
	if !a.enabled() {
		return "", true
	}
	if sid == "" {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[sid]
	if !ok || time.Now().After(s.expires) {
		delete(a.sessions, sid)
		return "", false
	}
	s.expires = time.Now().Add(a.ttl)
	a.sessions[sid] = s
	return s.csrf, true
}

// csrfOK проверяет CSRF-токен мутации против сессии.
func (a *authManager) csrfOK(sid, token string) bool {
	if !a.enabled() {
		return true
	}
	csrf, ok := a.check(sid)
	if !ok || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(csrf), []byte(token)) == 1
}

func (a *authManager) pruneLocked() {
	now := time.Now()
	for id, s := range a.sessions {
		if now.After(s.expires) {
			delete(a.sessions, id)
		}
	}
}

// setSessionCookie пишет httpOnly-сессию.
func setSessionCookie(w http.ResponseWriter, sid string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

// authHandler — middleware поверх ServeMux: rate-limit + сессии + CSRF.
//
// Публичные пути (не требуют сессии, но логин лимитируется):
//   - POST /api/login  — сам проверяет пароль;
//   - GET  /api/auth   — статус авторизации (для фронта, возвращает CSRF).
//
// Все остальные /api/* при включённой аутентификации требуют валидной сессии;
// мутации (POST/PUT/DELETE/PATCH) — ещё и X-CSRF-Token.
type authHandler struct {
	next      http.Handler
	auth      *authManager
	apiLim    *rateLimiter // общий лимит /api/*
	loginLim  *rateLimiter // строгий лимит входа
	chatLim   *rateLimiter // лимит чата (постинг запускает оркестрацию)
}

// ServeHTTP ограничивает и защищает входящие запросы.
func (h *authHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch path {
	case "/api/login":
		if !h.loginLim.allow(clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "слишком много попыток входа, попробуйте позже")
			return
		}
		h.next.ServeHTTP(w, r)
		return
	case "/api/auth":
		h.next.ServeHTTP(w, r)
		return
	}

	if strings.HasPrefix(path, "/api/") {
		if !h.apiLim.allow(clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "слишком много запросов, попробуйте позже")
			return
		}
		if strings.HasSuffix(path, "/chat") && !h.chatLim.allow(clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "слишком часто отправляете сообщения, попробуйте позже")
			return
		}
	}

	if h.auth == nil {
		h.next.ServeHTTP(w, r)
		return
	}

	if strings.HasPrefix(path, "/api/") {
		cookie, err := r.Cookie(authCookieName)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "требуется вход")
			return
		}
		sid := cookie.Value
		if _, ok := h.auth.check(sid); !ok {
			writeErr(w, http.StatusUnauthorized, "сессия истекла, выполните вход")
			return
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
			if !h.auth.csrfOK(sid, r.Header.Get(authCSRFHeader)) {
				writeErr(w, http.StatusForbidden, "неверный CSRF-токен")
				return
			}
		}
	}

	h.next.ServeHTTP(w, r)
}

// clientIP возвращает IP клиента из RemoteAddr (без порта).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- REST: вход/выход/статус аутентификации ---

// handleLogin проверяет пароль и выдаёт сессию + CSRF-токен.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.auth.enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "login": false, "csrf": ""})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}
	sid, csrf, ok := s.auth.login(body.Password)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "неверный пароль")
		return
	}
	setSessionCookie(w, sid, int(authSessionTTL/time.Second))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "login": true, "csrf": csrf})
}

// handleLogout уничтожает сессию и чистит cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(authCookieName); err == nil {
		s.auth.logout(c.Value)
	}
	setSessionCookie(w, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAuthStatus отдаёт фронту статус авторизации и текущий CSRF-токен
// (для уже существующей httpOnly-сессии, которую JS читать не может).
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if !s.auth.enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "login": false, "csrf": ""})
		return
	}
	if c, err := r.Cookie(authCookieName); err == nil {
		if csrf, ok := s.auth.check(c.Value); ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "login": true, "csrf": csrf})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": false, "login": true, "csrf": ""})
}