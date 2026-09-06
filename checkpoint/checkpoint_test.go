package checkpoint

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// newTestStore запускает in-memory Redis и создаёт хранилище чекпоинтов.
func newTestStore(t *testing.T, key string) *Store {
	t.Helper()
	srv := miniredis.RunT(t)

	store, err := NewStore(context.Background(), StoreConfig{
		Addr: srv.Addr(),
		Key:  key,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestSaveLoadRoundTrip(t *testing.T) {
	store := newTestStore(t, "checkpoint:test")
	defer store.Close()
	ctx := context.Background()

	planJSON := json.RawMessage(`{"summary":"s","steps":[]}`)
	snap := &Snapshot{
		ProjectName: "test",
		Summary:     "создать сервис",
		PlanJSON:    planJSON,
		Completed:   map[string]bool{"s1": true},
		Statuses:    map[string]string{"s1": StatusDone, "s2": StatusPending},
		Waves:       [][]string{{"s1"}, {"s2"}},
	}
	if err := store.Save(ctx, snap); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ProjectName != "test" || got.Summary != "создать сервис" {
		t.Fatalf("Load: project/summary mismatch: %#v", got)
	}
	if !got.Completed["s1"] {
		t.Fatal("Load: completed[s1] должно быть true")
	}
	if got.Statuses["s2"] != StatusPending {
		t.Fatal("Load: statuses[s2] должно быть pending")
	}
	if string(got.PlanJSON) != string(planJSON) {
		t.Fatalf("Load: PlanJSON mismatch: %s", got.PlanJSON)
	}
	if len(got.Waves) != 2 {
		t.Fatalf("Load: waves должен содержать 2 волны, got %#v", got.Waves)
	}
}

func TestLoadNotFound(t *testing.T) {
	store := newTestStore(t, "checkpoint:absent")
	defer store.Close()

	if _, err := store.Load(context.Background()); err != ErrNotFound {
		t.Fatalf("Load: ожидали ErrNotFound, got %v", err)
	}
}

func TestMarkStepDone(t *testing.T) {
	store := newTestStore(t, "checkpoint:test")
	defer store.Close()
	ctx := context.Background()

	planJSON := json.RawMessage(`{"steps":[]}`)
	if err := store.Save(ctx, &Snapshot{ProjectName: "test", PlanJSON: planJSON}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	snap, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.MarkStep(ctx, snap, "s1", StatusRunning); err != nil {
		t.Fatalf("MarkStep running: %v", err)
	}
	if snap.Completed["s1"] {
		t.Fatal("s1 не должен быть завершён на стадии running")
	}

	if err := store.MarkStep(ctx, snap, "s1", StatusDone); err != nil {
		t.Fatalf("MarkStep done: %v", err)
	}
	if !snap.Completed["s1"] {
		t.Fatal("s1 должен быть завершён после MarkStep done")
	}

	// Проверяем, что сохранилось в Redis.
	again, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load после MarkStep: %v", err)
	}
	if !again.Completed["s1"] || again.Statuses["s1"] != StatusDone {
		t.Fatalf("после MarkStep: completed=%v statuses=%v", again.Completed, again.Statuses)
	}
}

func TestIsCompleted(t *testing.T) {
	store := newTestStore(t, "checkpoint:test")
	defer store.Close()

	snap := &Snapshot{Completed: map[string]bool{"x": true}}
	if !store.IsCompleted(snap, "x") {
		t.Fatal("IsCompleted: x должен быть завершён")
	}
	if store.IsCompleted(snap, "y") {
		t.Fatal("IsCompleted: y не должен быть завершён")
	}
	if store.IsCompleted(nil, "x") {
		t.Fatal("IsCompleted(nil): должно быть false")
	}
}

func TestStoreKey(t *testing.T) {
	store := newTestStore(t, "checkpoint:custom")
	if store.Key() != "checkpoint:custom" {
		t.Fatalf("Key: got %q", store.Key())
	}
}