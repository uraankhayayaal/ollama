package lspclient

// Manager — кэш языковых серверов по (проект, стек). Долгоживущий процесс
// дорого запускать на каждый запрос: клиент поднимается лениво при первом
// обращении и переиспользуется, пока соединение живо. Мёртвый клиент
// перезапускается на следующем обращении.

import (
	"context"
	"path/filepath"
	"sync"

	"ai/stackdetect"
)

// Manager хранит активные клиенты. Потокобезопасен.
type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client
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
	return &Manager{clients: make(map[string]*Client)}
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

// Client возвращает живой клиент для проекта dir и стека kind, запуская
// сервер при необходимости.
func (m *Manager) Client(ctx context.Context, dir string, kind stackdetect.Kind) (*Client, error) {
	dir = filepath.Clean(dir)
	key := string(kind) + "|" + dir

	m.mu.Lock()
	if c, ok := m.clients[key]; ok && c.Alive() {
		m.mu.Unlock()
		return c, nil
	}
	delete(m.clients, key)
	m.mu.Unlock()

	c, err := Start(ctx, Config{Kind: kind, Dir: dir})
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if existing, ok := m.clients[key]; ok && existing.Alive() {
		m.mu.Unlock()
		_ = c.Close()
		return existing, nil
	}
	m.clients[key] = c
	m.mu.Unlock()
	return c, nil
}

// Close завершает все активные серверы.
func (m *Manager) Close() {
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for k, c := range m.clients {
		clients = append(clients, c)
		delete(m.clients, k)
	}
	m.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
}
