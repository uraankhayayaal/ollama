package rag

// Hermetic-тесты ProjectInfo (статус RAG-индекса, см. PLAN-architect-
// intelligence.md, Ф-1/Р-2): Count API с фильтром по проекту, degrade при
// недоступном Qdrant, 0 чанков для непроиндексированного/несуществующей
// коллекции. Сеть не используется (fakeStore из client_test.go).

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// У проиндексированного проекта ProjectInfo возвращает число чанков
// (Count API, фильтр по project_name).
func TestProjectInfoCountsChunks(t *testing.T) {
	store := &fakeStore{exists: true, countResult: 42}
	c := newClient(store, &fakeEmbedder{dim: 8}, DefaultCollectionName)

	info, err := c.ProjectInfo(context.Background(), "billingService")
	if err != nil {
		t.Fatalf("ProjectInfo: %v", err)
	}
	if info.Project != "billingService" || info.Chunks != 42 {
		t.Fatalf("info: got %+v, want {billingService 42}", info)
	}
	if !info.Indexed() {
		t.Fatal("42 чанка должны считаться проиндексированными")
	}
	if store.countCalls != 1 {
		t.Fatalf("Count должен вызываться один раз, got %d", store.countCalls)
	}
}

// Непроиндексированный проект — 0 чанков (Indexed() == false), без ошибки.
func TestProjectInfoEmptyIndex(t *testing.T) {
	store := &fakeStore{exists: true, countResult: 0}
	c := newClient(store, &fakeEmbedder{dim: 8}, DefaultCollectionName)

	info, err := c.ProjectInfo(context.Background(), "newproj")
	if err != nil {
		t.Fatalf("ProjectInfo пустого индекса: %v", err)
	}
	if info.Chunks != 0 || info.Indexed() {
		t.Fatalf("пустой индекс: got %+v, want 0 чанков", info)
	}
}

// Коллекции ещё нет — 0 чанков, Count не вызывается.
func TestProjectInfoNoCollection(t *testing.T) {
	store := &fakeStore{exists: false}
	c := newClient(store, &fakeEmbedder{dim: 8}, DefaultCollectionName)

	info, err := c.ProjectInfo(context.Background(), "newproj")
	if err != nil {
		t.Fatalf("ProjectInfo без коллекции: %v", err)
	}
	if info.Chunks != 0 || info.Indexed() {
		t.Fatalf("без коллекции: got %+v, want 0 чанков", info)
	}
	if store.countCalls != 0 {
		t.Fatalf("Count не должен вызываться без коллекции, got %d", store.countCalls)
	}
}

// Недоступный Qdrant — UnavailableError (инструмент превратит в skipped), а не
// падение.
func TestProjectInfoUnavailable(t *testing.T) {
	store := &fakeStore{exists: true, countErr: errors.New("connection refused")}
	c := newClient(store, &fakeEmbedder{dim: 8}, DefaultCollectionName)

	_, err := c.ProjectInfo(context.Background(), "proj")
	if !IsUnavailable(err) {
		t.Fatalf("ошибка должна быть UnavailableError, got %T: %v", err, err)
	}
}

// Пустое имя проекта — понятная ошибка без обращения к хранилищу.
func TestProjectInfoEmptyProject(t *testing.T) {
	c := newClient(&fakeStore{}, &fakeEmbedder{dim: 8}, DefaultCollectionName)
	_, err := c.ProjectInfo(context.Background(), "  ")
	if err == nil || !strings.Contains(err.Error(), "проект") {
		t.Fatalf("ожидалась ошибка про пустое имя проекта, got %v", err)
	}
}