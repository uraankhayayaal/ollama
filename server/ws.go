// Package server — минимальная обёртка WebSocket (RFC 6455) без внешних
// зависимостей. Поддерживает handshake, текстовые кадры (fin + opcode 0x1),
// ping/pong, close. Маскировка клиент→сервер, сервер отвечает без маски.
package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WsConn — websocket-соединение поверх net.Conn с буферизованным чтением.
// Запись кадров безопасна для конкурентных вызовов (мутекс).
type WsConn struct {
	conn      net.Conn
	br        *bufio.Reader
	wmu       sync.Mutex
	closed    chan struct{}
	closeOnce sync.Once
}

// WsUpgrade выполняет HTTP→WS upgrade и возвращает WsConn.
func WsUpgrade(w http.ResponseWriter, r *http.Request) (*WsConn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "bad Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, fmt.Errorf("ws: нет Sec-WebSocket-Key")
	}
	if !strings.EqualFold(r.Header.Get("Connection"), "upgrade") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "bad upgrade headers", http.StatusBadRequest)
		return nil, fmt.Errorf("ws: неверные заголовки upgrade")
	}

	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "unsupported", http.StatusInternalServerError)
		return nil, fmt.Errorf("ws: Hijack не поддерживается")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("ws: hijack: %w", err)
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := brw.WriteString(resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ws: запись handshake: %w", err)
	}
	if err := brw.Flush(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ws: flush handshake: %w", err)
	}
	return &WsConn{
		conn:   conn,
		br:     brw.Reader,
		closed: make(chan struct{}),
	}, nil
}

// WriteText отправляет текстовый кадр (opcode 0x1, без маски).
func (c *WsConn) WriteText(payload []byte) error {
	return c.writeFrame(0x1, payload)
}

// WriteClose отправляет close-кадр с кодом 1000.
func (c *WsConn) WriteClose() error {
	code := []byte{0x03, 0xe8} // 1000 big-endian
	err := c.writeFrame(0x8, code)
	c.closeOnce.Do(func() { close(c.closed) })
	return err
}

// ReadBlock читает один кадр и возвращает opcode и payload. Если opcode 0x8
// (close) — возвращает io.EOF. Ping автоматически отвечает pong.
func (c *WsConn) ReadBlock() (opcode byte, payload []byte, err error) {
	for {
		op, pay, er := c.readFrame()
		if er != nil {
			return 0, nil, er
		}
		switch op {
		case 0x8:
			return 0, nil, io.EOF
		case 0x9:
			// ping → pong
			_ = c.writeFrame(0xA, pay)
			continue
		case 0xA:
			// pong — игнорируем
			continue
		default:
			return op, pay, nil
		}
	}
}

// SetDeadline устанавливает таймаут чтения/записи.
func (c *WsConn) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}

// RemoteAddr возвращает адрес клиента.
func (c *WsConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

// Close закрывает соединение (идемпотентно).
func (c *WsConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.conn.Close()
}

// writeFrame записывает один WS-кадр. Не маскирует (сервер → клиент).
func (c *WsConn) writeFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	n := len(payload)

	// Буфер: 2 + extended length (если нужно) + payload
	// Максимальный размер фрейма — 2 + 8 + len(payload) ≤ 20 + ~1MB.
	hdr := make([]byte, 2, 20)
	hdr[0] = 0x80 | opcode // FIN=1

	switch {
	case n <= 125:
		hdr[1] = byte(n)
	case n <= 65535:
		hdr[1] = 126
		hdr = append(hdr, byte(n>>8), byte(n))
	default:
		hdr[1] = 127
		for i := 7; i >= 0; i-- {
			hdr = append(hdr, byte(n>>(8*i)))
		}
	}

	hdr = append(hdr, payload...)
	_, err := c.conn.Write(hdr)
	if err != nil {
		c.closeOnce.Do(func() { close(c.closed) })
	}
	return err
}

// readFrame читает один WS-кадр. Ожидает маскировку от клиента.
func (c *WsConn) readFrame() (opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if err := c.readExact(hdr[:], 2); err != nil {
		return 0, nil, err
	}
	opcode = hdr[0] & 0x0F
	mask := hdr[1]&0x80 != 0
	length := int64(hdr[1] & 0x7F)

	switch length {
	case 126:
		var ext [2]byte
		if err := c.readExact(ext[:], 2); err != nil {
			return 0, nil, err
		}
		length = int64(ext[0])<<8 | int64(ext[1])
	case 127:
		var ext [8]byte
		if err := c.readExact(ext[:], 8); err != nil {
			return 0, nil, err
		}
		length = 0
		for _, b := range ext {
			length = length<<8 | int64(b)
		}
	}

	if length > 1<<20 { // 1 MB лимит
		return 0, nil, fmt.Errorf("ws: слишком большой кадр: %d байт", length)
	}

	var maskKey [4]byte
	if mask {
		if err := c.readExact(maskKey[:], 4); err != nil {
			return 0, nil, err
		}
	}

	payload = make([]byte, length)
	if err := c.readExact(payload, int(length)); err != nil {
		return 0, nil, err
	}
	if mask {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}

// readExact читает ровно n байт из буфера. Если len(dst) < n — ошибка.
func (c *WsConn) readExact(dst []byte, n int) error {
	if len(dst) < n {
		return fmt.Errorf("ws: readExact: буфер %d < %d", len(dst), n)
	}
	_, err := io.ReadFull(c.br, dst[:n])
	if err != nil {
		c.closeOnce.Do(func() { close(c.closed) })
	}
	return err
}

// IsClosed возвращает true, если соединение закрыто.
func (c *WsConn) IsClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}
