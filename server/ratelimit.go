// Rate-limit Web UI (Ф-3): простой per-IP fixed-window лимитер без внешних
// зависимостей. Защищает (а) вход по паролю от грубого перебора и (б) API от
// спама. Лимиты константные, окно 1 минута.

package server

import (
	"sync"
	"time"
)

// rlWindow — счётчик запросов в текущем окне.
type rlWindow struct {
	count int
	reset time.Time
}

// rateLimiter — fixed-window лимитер по ключу (обычно IP клиента).
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*rlWindow
}

// newRateLimit создаёт лимитер: не более limit запросов в окне window на ключ.
func newRateLimit(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, hits: map[string]*rlWindow{}}
}

// allow увеличивает счётчик ключа и сообщает, не превышен ли лимит.
// Негативный/нулевой limit — лимитирование отключено.
func (r *rateLimiter) allow(key string) bool {
	if r == nil || r.limit <= 0 {
		return true
	}
	if key == "" {
		key = "unknown"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	w, ok := r.hits[key]
	if !ok || now.After(w.reset) {
		w = &rlWindow{reset: now.Add(r.window)}
		r.hits[key] = w
	}
	w.count++
	return w.count <= r.limit
}