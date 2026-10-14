package logs

import (
	"sync"
	"time"
)

// Границы кольцевого буфера логов рантайма. Меньше — потеряем причину падения
// (она всегда в конце), больше — распухает память на проектах с болтливым
// выводом вроде сборки фронтенда.
const (
	BufferLines = 2000
	DefaultTail = 300
	MaxTail     = 2000
)

// Line — строка лога рантайма в API.
type Line struct {
	// Time — момент получения строки (не время приложения): логи сторонних
	// приложений часто вообще без таймстемпов.
	Time time.Time `json:"time"`
	// Source — откуда строка: local (процесс) или docker (сервис compose).
	Source string `json:"source,omitempty"`
	// Line — сама строка.
	Line string `json:"line"`
}

// RingBuffer — потокобезопасный кольцевой буфер строк логов рантайма проекта:
// хранит последние BufferLines строк, Tail отдаёт хвост (с дефолтом и потолком),
// TakeDirty сообщает, есть ли несохранённые строки для лога проекта.
type RingBuffer struct {
	mu    sync.Mutex
	lines []Line
	// dirty — есть несохранённые строки (для лога проекта).
	dirty int
}

// Push добавляет строку в буфер, вытесняя старые за границей кольца.
func (b *RingBuffer) Push(source, line string) {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, Line{Time: now, Source: source, Line: line})
	if len(b.lines) > BufferLines {
		n := copy(b.lines, b.lines[len(b.lines)-BufferLines:])
		b.lines = b.lines[:n]
	}
	b.dirty++
}

// Tail возвращает последние n строк (n <= 0 — умолчание, выше потолка —
// потолок).
func (b *RingBuffer) Tail(n int) []Line {
	if n <= 0 {
		n = DefaultTail
	}
	if n > MaxTail {
		n = MaxTail
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > len(b.lines) {
		n = len(b.lines)
	}
	out := make([]Line, n)
	copy(out, b.lines[len(b.lines)-n:])
	return out
}

// TakeDirty забирает и обнуляет счётчик несохранённых строк: вернёт true, если
// в лог проекта есть что дописать.
func (b *RingBuffer) TakeDirty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dirty == 0 {
		return false
	}
	b.dirty = 0
	return true
}
