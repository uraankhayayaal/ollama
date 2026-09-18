// LogBroker — перематывает лог-файлы и транслирует новые строки в
// WebSocket-хаб (pub/sub per project, type="log").

package server

import (
	"context"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

// logMessage — полезная нагрузка события "log" в WebSocket-хабе.
type logMessage struct {
	Project string `json:"project"`
	File    string `json:"file"`
	Line    string `json:"line"`
}

// subFile — экешение для перематывания одного файла.
type subFile struct {
	file      *os.File
	pos       int64
	remainder string
}

// logSubscriber — подписка проекта.
type logSubscriber struct {
	project string
	files   map[string]*subFile
}

// logBroker — per-project log watcher.
type logBroker struct {
	mu          sync.Mutex
	subscribers map[string]*logSubscriber
	hub         *Hub
}

func newLogBroker(h *Hub) *logBroker {
	return &logBroker{
		subscribers: make(map[string]*logSubscriber),
		hub:         h,
	}
}

// Run запускает фоновое сканировние. Выполняется до ctx.Done.
func (b *logBroker) Run(ctx context.Context) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	done := ctx.Done()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			b.broadcast()
		}
	}
}

// Subscribe открывает файлы (файл → position = end) для проекта.
func (b *logBroker) Subscribe(project string, dirs []string, files []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscribers[project] != nil {
		// Уже подписался.
		return
	}
	m := make(map[string]*subFile)
	for _, dir := range dirs {
		for _, f := range files {
			path := dir + "/" + f
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			r, err := os.Open(path)
			if err != nil {
				continue
			}
			// Seek to the current amount so that we follow only the new data
			io.CopyN(io.Discard, r, info.Size())
			m[dir+"::"+f] = &subFile{
				file: r,
				pos:  info.Size(),
			}
		}
	}
	b.subscribers[project] = &logSubscriber{
		project: project,
		files:   m,
	}
	log.Printf("logbroker: project=%s files=%d", project, len(m))
}

func (b *logBroker) broadcast() {
	b.mu.Lock()
	subs := make([]*logSubscriber, 0, len(b.subscribers))
	for _, s := range b.subscribers {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	for _, sub := range subs {
		for k, sf := range sub.files {
			dir, fname := splitKey(k)
			if dir == "" {
				continue
			}
			info, err := os.Stat(sf.file.Name())
			if err != nil || info.Size() < sf.pos {
				// ротация.
				sf.file.Close()
				sf.file = nil
				sf.pos = 0
				sf.remainder = ""
				continue
			}
			n := info.Size() - sf.pos
			if n == 0 {
				continue
			}
			buf := make([]byte, n)
			nn, err := io.ReadFull(sf.file, buf)
			if err != nil && err != io.ErrUnexpectedEOF {
				continue
			}
data := append([]byte(sf.remainder), buf[:nn]...)
		sf.remainder = ""
			var lines []string
			i := 0
			for i < len(data) {
				j := i
				for j < len(data) && data[j] != '\n' {
					j++
				}
				if j < len(data) {
					lines = append(lines, string(data[i:j]))
					i = j + 1
				} else {
					break
				}
			}
			if i < len(data) {
				sf.remainder = string(data[i:])
			}
			for _, ln := range lines {
				b.hub.publish(sub.project, "log", logMessage{
					Project: sub.project,
					File:    fname,
					Line:    ln,
				})
			}
			sf.pos = info.Size()
		}
	}
}

// splitKey разбивает"dir::name" на dir и file.
func splitKey(key string) (dir, file string) {
	if i := len(key) - 1; i >= 1 && key[i] == ':' && key[i-1] == ':' {
		return key[:i-1], key[i+1:]
	}
	return "", key
}