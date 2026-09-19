package tokens

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func newTestStore(t *testing.T, project string) *Store {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	s := NewStoreNoCheck(StoreConfig{Addr: mr.Addr(), Project: project})
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAddAndGet(t *testing.T) {
	s := newTestStore(t, "proj-a")
	ctx := context.Background()

	in, out, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if in != 0 || out != 0 {
		t.Fatalf("стартовые суммы = %d/%d, want 0/0", in, out)
	}

	totIn, totOut, err := s.Add(ctx, 100, 40)
	if err != nil {
		t.Fatal(err)
	}
	if totIn != 100 || totOut != 40 {
		t.Fatalf("после первого Add = %d/%d, want 100/40", totIn, totOut)
	}
	totIn, totOut, err = s.Add(ctx, 50, 60)
	if err != nil {
		t.Fatal(err)
	}
	if totIn != 150 || totOut != 100 {
		t.Fatalf("после второго Add = %d/%d, want 150/100", totIn, totOut)
	}

	in, out, _ = s.Get(ctx)
	if in != 150 || out != 100 {
		t.Fatalf("Get = %d/%d, want 150/100", in, out)
	}
}

func TestPerProjectCounters(t *testing.T) {
	a := newTestStore(t, "proj-a")
	b := newTestStore(t, "proj-b")
	ctx := context.Background()

	if _, _, err := a.Add(ctx, 10, 5); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Add(ctx, 2, 1); err != nil {
		t.Fatal(err)
	}

	if in, out, _ := a.Get(ctx); in != 10 || out != 5 {
		t.Fatalf("счётчик proj-a = %d/%d, want 10/5", in, out)
	}
	if in, out, _ := b.Get(ctx); in != 2 || out != 1 {
		t.Fatalf("счётчик proj-b = %d/%d, want 2/1", in, out)
	}
}

func TestPersistsAcrossStores(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	ctx := context.Background()

	s1 := NewStoreNoCheck(StoreConfig{Addr: mr.Addr(), Project: "proj"})
	if _, _, err := s1.Add(ctx, 7, 3); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	// Новое хранилище на том же Redis видит накопленные суммы (жизненный цикл
	// проекта переживает перезапуск сервера).
	s2 := NewStoreNoCheck(StoreConfig{Addr: mr.Addr(), Project: "proj"})
	t.Cleanup(func() { s2.Close() })
	if in, out, _ := s2.Get(ctx); in != 7 || out != 3 {
		t.Fatalf("s2.Get = %d/%d, want 7/3", in, out)
	}
}
