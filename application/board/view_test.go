package board

import (
	"context"
	"errors"
	"strings"
	"testing"

	iboard "ai/infrastructure/board"
)

// fakeStore — заглушка Store с настраиваемыми наборами и ошибками.
type fakeStore struct {
	epics []*iboard.Epic
	tasks []*iboard.Task
	meta  *iboard.Meta
	err   error
}

func (f *fakeStore) ListEpics(context.Context) ([]*iboard.Epic, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.epics, nil
}

func (f *fakeStore) ListTasks(context.Context) ([]*iboard.Task, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tasks, nil
}

func (f *fakeStore) GetMeta(context.Context) (*iboard.Meta, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.meta, nil
}

func epic(id string, priority int) *iboard.Epic {
	return &iboard.Epic{TaskSpec: iboard.TaskSpec{TaskID: id}, Priority: priority}
}

func TestViewOrdersEpicsAndCollects(t *testing.T) {
	store := &fakeStore{
		epics: []*iboard.Epic{epic("EP-1", 1), epic("EP-3", 3), epic("EP-2", 5)},
		tasks: []*iboard.Task{{TaskSpec: iboard.TaskSpec{TaskID: "T-1"}}},
		meta:  &iboard.Meta{ProjectName: "proj"},
	}
	v, err := View(context.Background(), store)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if v.Meta == nil || v.Meta.ProjectName != "proj" {
		t.Fatalf("meta = %+v", v.Meta)
	}
	if len(v.Tasks) != 1 {
		t.Fatalf("tasks = %d", len(v.Tasks))
	}
	// Порядок по приоритету (убывание): 5, 3, 1.
	got := []string{v.Epics[0].TaskID, v.Epics[1].TaskID, v.Epics[2].TaskID}
	want := []string{"EP-2", "EP-3", "EP-1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if v.Total != nil {
		t.Fatal("View не должен выставлять Total (это делает ViewPage)")
	}
}

func TestViewPropagatesError(t *testing.T) {
	_, err := View(context.Background(), &fakeStore{err: errors.New("boom")})
	if err == nil {
		t.Fatal("ожидалась ошибка store")
	}
}

func TestViewPageFullAndPaged(t *testing.T) {
	store := &fakeStore{
		epics: []*iboard.Epic{epic("EP-1", 3), epic("EP-2", 2), epic("EP-3", 1)},
		tasks: []*iboard.Task{
			{TaskSpec: iboard.TaskSpec{TaskID: "T-1"}},
			{TaskSpec: iboard.TaskSpec{TaskID: "T-2"}},
			{TaskSpec: iboard.TaskSpec{TaskID: "T-3"}},
			{TaskSpec: iboard.TaskSpec{TaskID: "T-4"}},
		},
	}

	full, err := ViewPage(context.Background(), store, 0, 0)
	if err != nil {
		t.Fatalf("ViewPage full: %v", err)
	}
	if full.Total == nil || full.Total.Epics != 3 || full.Total.Tasks != 4 {
		t.Fatalf("total = %+v", full.Total)
	}
	if len(full.Epics) != 3 || len(full.Tasks) != 4 {
		t.Fatal("полный снимок не должен обрезаться")
	}

	page, err := ViewPage(context.Background(), store, 2, 1)
	if err != nil {
		t.Fatalf("ViewPage page: %v", err)
	}
	if len(page.Epics) != 2 || len(page.Tasks) != 2 {
		t.Fatalf("страница = epics %d, tasks %d", len(page.Epics), len(page.Tasks))
	}
	if page.Total == nil || page.Total.Tasks != 4 {
		t.Fatalf("страница total = %+v", page.Total)
	}
	if page.Tasks[0].TaskID != "T-2" || page.Tasks[1].TaskID != "T-3" {
		t.Fatalf("смещение страницы = %s,%s", page.Tasks[0].TaskID, page.Tasks[1].TaskID)
	}

	// Отрицательный offset нормализуется к 0.
	neg, _ := ViewPage(context.Background(), store, 1, -5)
	if len(neg.Tasks) != 1 || neg.Tasks[0].TaskID != "T-1" {
		t.Fatalf("neg offset = %+v", neg.Tasks)
	}

	// Пустая страница за пределами.
	beyond, _ := ViewPage(context.Background(), store, 2, 99)
	if len(beyond.Epics) != 0 || len(beyond.Tasks) != 0 {
		t.Fatal("страница за пределами должна быть пустой")
	}
}

func TestSlicePage(t *testing.T) {
	in := []int{1, 2, 3, 4}
	if got := SlicePage(in, 1, 2); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("SlicePage = %v", got)
	}
	if got := SlicePage(in, 0, 99); len(got) != 4 {
		t.Fatalf("clamp = %v", got)
	}
	if got := SlicePage(in, 10, 2); got == nil || len(got) != 0 {
		t.Fatalf("empty = %#v, want непустой пустой срез", got)
	}
}

func TestTruncateText(t *testing.T) {
	if got := TruncateText("short", 10); got != "short" {
		t.Fatalf("no-truncate = %q", got)
	}
	if got := TruncateText("abcdef", 3); got != "abc…" || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncate = %q", got)
	}
}
