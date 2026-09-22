package lspclient

// Manager — кэш языковых серверов по (проект, стек). Долгоживущий процесс
// дорого запускать на каждый запрос: клиент поднимается лениво при первом
// обращении и переиспользуется, пока соединение живо. Мёртвый клиент
// перезапускается на следующем обращении.
//
// Оптимизации:
//   - singleflight запуска: параллельные обращения к одному ключу не стартуют
//     N процессов gopls — первый стартует, остальные получают готовый клиент;
//   - эвикция простоя: клиенты, не использовавшиеся дольше LSP_IDLE_TTL,
//     закрываются на ближайшем обращении (или через Manager.CloseIdle вручную),
//     чтобы долгоживущий процесс не копил серверы по всем открытым проектам.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai/stackdetect"
)

// lspStart запускает клиент; подменяется в тестах (не стартует процессы).
var lspStart = Start

// startCall — идущий запуск клиента: ожидающие ждут done, результат ошибки —
// в err (кто-то один стартует, остальные переиспользуют).
type startCall struct {
	done chan struct{}
	err  error
}

// Manager хранит активные клиенты. Потокобезопасен.
type Manager struct {
	mu        sync.Mutex
	clients   map[string]*Client
	lastUsed  map[string]time.Time
	inflight  map[string]*startCall
	lastReap  time.Time
}

var (
	sharedOnce sync.Once
	sharedMgr  *Manager
)

// Shared возвращает общий (процессный) менеджер клиентов.
func Shared() *Manager {
	sharedOnce.Do(func() { sharedMgr = NewManager() })
	return sharedMgr
}

// NewManager создаёт пустой менеджер.
func NewManager() *Manager {
	return &Manager{
		clients:  make(map[string]*Client),
		lastUsed: make(map[string]time.Time),
		inflight: make(map[string]*startCall),
	}
}

// Navigator возвращает живой клиент для проекта dir и стека kind, запуская
// сервер при необходимости.
func (m *Manager) Navigator(ctx context.Context, dir string, kind stackdetect.Kind) (Navigator, error) {
	return m.Client(ctx, dir, kind)
}

// DiagnosticsProvider возвращает клиент как источник нативных диагностик.
func (m *Manager) DiagnosticsProvider(ctx context.Context, dir string, kind stackdetect.Kind) (DiagnosticsProvider, error) {
	return m.Client(ctx, dir, kind)
}

// Outliner возвращает клиент как источник оглавлений файлов (documentSymbol).
func (m *Manager) Outliner(ctx context.Context, dir string, kind stackdetect.Kind) (Outliner, error) {
	return m.Client(ctx, dir, kind)
}

// Client возвращает живой клиент для проекта dir и стека kind, запуская
// сервер при необходимости. Параллельные обращения к одному ключу дедупли
// цируются (singleflight): один стартует, остальные ждут готовый клиент.
func (m *Manager) Client(ctx context.Context, dir string, kind stackdetect.Kind) (*Client, error) {
	dir = filepath.Clean(dir)
	key := string(kind) + "|" + dir

	for {
		m.mu.Lock()
		if c, ok := m.clients[key]; ok && c.Alive() {
			m.lastUsed[key] = time.Now()
			m.mu.Unlock()
			m.maybeReapIdle()
			return c, nil
		}
		if sf, ok := m.inflight[key]; ok {
			m.mu.Unlock()
			<-sf.done
			m.mu.Lock()
			c, ok := m.clients[key]
			err := sf.err
			m.mu.Unlock()
			if ok && c.Alive() {
				return c, nil
			}
			if err != nil {
				return nil, err
			}
			continue // клиент успел умереть/исчезнуть — пробуем ещё раз
		}
		sf := &startCall{done: make(chan struct{})}
		m.inflight[key] = sf
		m.mu.Unlock()
		// Мы — стартующий.
		c, err := lspStart(ctx, Config{Kind: kind, Dir: dir})

		m.mu.Lock()
		delete(m.inflight, key)
		var loser *Client
		if err == nil {
			if existing, ok := m.clients[key]; ok && existing.Alive() {
				loser = c
			} else {
				m.clients[key] = c
				m.lastUsed[key] = time.Now()
			}
		}
		sf.err = err
		close(sf.done)
		m.mu.Unlock()

		if loser != nil {
			_ = loser.Close()
			c = m.clients[key]
		}
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}

// reapInterval — как часто Client() делает фоновый проход эвикции простоя.
const reapInterval = time.Minute

// idleTTL — таймаут простоя клиента (LSP_IDLE_TTL, по умолчанию 10m).
func idleTTL() time.Duration {
	if v := strings.TrimSpace(os.Getenv("LSP_IDLE_TTL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Minute
		}
	}
	return 10 * time.Minute
}

// maybeReapIdle закрывает неиспользуемые клиенты на ближайшем обращении
// (не чаще раза в минуту, дешёвый проход по lastUsed).
func (m *Manager) maybeReapIdle() {
	ttl := idleTTL()
	if ttl <= 0 {
		return
	}
	m.mu.Lock()
	if time.Since(m.lastReap) < reapInterval {
		m.mu.Unlock()
		return
	}
	m.lastReap = time.Now()
	m.mu.Unlock()
	m.reapIdle(ttl)
}

// CloseIdle закрывает клиентов, не использовавшихся дольше ttl. Возвращает
// число закрытых. Полезен для явной «уборки» в долгоживущих процессах.
func (m *Manager) CloseIdle(ttl time.Duration) int { return m.reapIdle(ttl) }

func (m *Manager) reapIdle(ttl time.Duration) int {
	m.mu.Lock()
	now := time.Now()
	var idle []*Client
	for k, lu := range m.lastUsed {
		if now.Sub(lu) > ttl {
			if c, ok := m.clients[k]; ok {
				idle = append(idle, c)
				delete(m.clients, k)
				delete(m.lastUsed, k)
			}
		}
	}
	m.mu.Unlock()
	for _, c := range idle {
		_ = c.Close()
	}
	return len(idle)
}

// Close завершает все активные серверы.
func (m *Manager) Close() {
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for k, c := range m.clients {
		clients = append(clients, c)
		delete(m.clients, k)
		delete(m.lastUsed, k)
	}
	m.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
}
