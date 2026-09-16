package server

import (
	"encoding/json"
	"log"
	"sync"
)

// Hub — WebSocket-хаб: публикует события клиентам по проекту.
// Каждый проект имеет свой набор подписчиков; publish рассылает JSON
// {type, payload} всем клиентам проекта.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]map[*wsClient]struct{}
}

// wsClient — WS-подключение проекта: writeLoop принимает сообщения из
// канала send, readLoop обрабатывает control frames (ping/close).
type wsClient struct {
	conn    *WsConn
	send    chan []byte // буфер 64 — при переполнении клиент отключается
	project string
	hub     *Hub
	closeCh chan struct{}
	close   sync.Once
}

// NewHub создаёт пустой хаб.
func NewHub() *Hub {
	return &Hub{clients: make(map[string]map[*wsClient]struct{})}
}

// Subscribe регистрирует клиент и запускает read/write-горутины.
func (h *Hub) Subscribe(project string, c *WsConn) {
	cl := &wsClient{
		conn:    c,
		send:    make(chan []byte, 64),
		project: project,
		hub:     h,
		closeCh: make(chan struct{}),
	}
	h.mu.Lock()
	if h.clients[project] == nil {
		h.clients[project] = make(map[*wsClient]struct{})
	}
	h.clients[project][cl] = struct{}{}
	h.mu.Unlock()

	go cl.readLoop()
	go cl.writeLoop()
}

// Publish рассылает событие (type + payload) всем клиентам проекта.
func (h *Hub) Publish(project, typ string, payload any) {
	data, err := json.Marshal(struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}{typ, payload})
	if err != nil {
		log.Printf("ws hub: marshal event %s/%s: %v", project, typ, err)
		return
	}
	h.mu.RLock()
	clients := make([]*wsClient, 0, len(h.clients[project]))
	for cl := range h.clients[project] {
		clients = append(clients, cl)
	}
	h.mu.RUnlock()

	for _, cl := range clients {
		// Неблокирующая отправка: если клиент «тормозит» — отключаем.
		select {
		case cl.send <- data:
		default:
			cl.shutdown()
		}
	}
}

// publish — короткий алиас Publish (используется в session.go).
func (h *Hub) publish(project, typ string, payload any) { h.Publish(project, typ, payload) }

// Unregister удаляет клиент из хаба (вызывается из readLoop/writeLoop).
func (h *Hub) Unregister(cl *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if subs, ok := h.clients[cl.project]; ok {
		delete(subs, cl)
		if len(subs) == 0 {
			delete(h.clients, cl.project)
		}
	}
}

// CloseAll закрывает все соединения (при завершении сервера).
func (h *Hub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for project := range h.clients {
		for cl := range h.clients[project] {
			cl.shutdown()
		}
		delete(h.clients, project)
	}
}

// --- wsClient ---

// writeLoop читает из send и отправляет кадры клиенту. Завершается при ошибке
// записи или закрытии.
func (cl *wsClient) writeLoop() {
	defer cl.shutdown()
	for {
		select {
		case <-cl.closeCh:
			return
		case data, ok := <-cl.send:
			if !ok {
				return
			}
			if err := cl.conn.WriteText(data); err != nil {
				return
			}
		}
	}
}

// readLoop читает кадры (ping/pong/close) и поддерживает соединение
// активным. При ошибке чтения (разрыв) — отключает клиента.
func (cl *wsClient) readLoop() {
	defer cl.shutdown()
	// readBlock блокируется на чтении; при закрытии conn вернёт ошибку.
	for {
		op, pay, err := cl.conn.ReadBlock()
		if err != nil {
			return
		}
		// Текстовые кадры от клиента нам не нужны (сервер не принимает данные).
		_ = op
		_ = pay
	}
}

// shutdown закрывает соединение и удаляет из хаба (идемпотентно).
func (cl *wsClient) shutdown() {
	cl.close.Do(func() {
		close(cl.closeCh)
		cl.conn.WriteClose()
		cl.conn.Close()
		cl.hub.Unregister(cl)
	})
}
