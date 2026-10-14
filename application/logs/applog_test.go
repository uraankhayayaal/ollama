package logs

import "testing"

func TestRingBufferWrapsAndTails(t *testing.T) {
	var b RingBuffer
	for i := 0; i < BufferLines+50; i++ {
		b.Push("local", "line")
	}
	if got := len(b.Tail(0)); got != DefaultTail {
		t.Fatalf("умолчание: %d строк, ждём %d", got, DefaultTail)
	}
	if got := len(b.Tail(10)); got != 10 {
		t.Fatalf("Tail(10): %d", got)
	}
	// Буфер кольцевой: всего в нём не больше BufferLines.
	if total := b.Tail(MaxTail); len(total) != BufferLines {
		t.Fatalf("в буфере %d строк, ждём %d", len(total), BufferLines)
	}
}

func TestRingBufferTailBounds(t *testing.T) {
	var b RingBuffer
	for i := 0; i < MaxTail+100; i++ {
		b.Push("local", "x")
	}
	if got := len(b.Tail(-5)); got != DefaultTail {
		t.Fatalf("отрицательный lines: %d, ждём дефолт %d", got, DefaultTail)
	}
	if got := len(b.Tail(MaxTail * 10)); got != MaxTail {
		t.Fatalf("lines выше потолка не ограничен: %d, ждём %d", got, MaxTail)
	}
}

func TestRingBufferEmptyAndDirty(t *testing.T) {
	var b RingBuffer
	if got := b.Tail(10); len(got) != 0 {
		t.Fatalf("пустой буфер отдал %d строк", len(got))
	}
	if b.TakeDirty() {
		t.Fatalf("пустой буфер не должен быть dirty")
	}
	b.Push("local", "x")
	if !b.TakeDirty() {
		t.Fatalf("после Push буфер должен быть dirty")
	}
	if b.TakeDirty() {
		t.Fatalf("повторный TakeDirty должен вернуть false")
	}
}

func TestRingBufferKeepsSource(t *testing.T) {
	var b RingBuffer
	b.Push("docker", "hello")
	got := b.Tail(1)
	if len(got) != 1 || got[0].Source != "docker" || got[0].Line != "hello" {
		t.Fatalf("строка потеряла источник/текст: %+v", got)
	}
	if got[0].Time.IsZero() {
		t.Fatalf("строка без времени получения")
	}
}
