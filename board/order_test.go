package board

import (
	"context"
	"testing"
)

// TestEpicPhase классифицирует эпики по фазе разработки по роли лида
// (маркеры совпадают с isRole): тестирование — последняя фаза, инфраструктура
// (DevOps) — первая, всё остальное — приложение.
func TestEpicPhase(t *testing.T) {
	cases := []struct {
		role string
		want string
	}{
		{"qaengineer", "test"},
		{"QA-инженер", "test"},
		{"Testing lead", "test"},
		{"devops engineer", "infra"},
		{"инфраструктура", "infra"},
		{"SRE", "infra"},
		{"docker", "infra"},
		{"ci/cd", "infra"},
		{"backend developer", "app"},
		{"frontend", "app"},
		{"", "app"},
		{"архитектор", "app"},
	}
	for _, c := range cases {
		if got := EpicPhase(&Epic{TaskSpec: TaskSpec{AssignedRole: c.role}}); got != c.want {
			t.Errorf("EpicPhase(%q) = %q, want %q", c.role, got, c.want)
		}
	}
}

func epic(seq int, prio int, id, role string) *Epic {
	return &Epic{
		TaskSpec: TaskSpec{
			TaskID:        id,
			Title:         "эпик " + id,
			AssignedRole:  role,
			SequenceOrder: FlexInt(seq),
		},
		Priority: prio,
	}
}

// TestEpicLess фаза первична: инфраструктура раньше приложения и тестирования
// независимо от приоритета и sequence_order; внутри фазы — приоритет по
// убыванию, затем sequence_order, затем task_id.
func TestEpicLess(t *testing.T) {
	// Инфраструктура опережает приложение даже при меньшем приоритете.
	if !EpicLess(epic(1, 1, "INFRA-1", "devops"), epic(1, 9, "APP-1", "backend")) {
		t.Error("инфраструктура должна идти раньше приложения (QA Last, фаза первична)")
	}
	// Приложение — раньше тестирования.
	if !EpicLess(epic(1, 1, "APP-1", "backend"), epic(1, 9, "TEST-1", "qa")) {
		t.Error("приложение должно идти раньше тестирования")
	}
	// Внутри фазы приоритет по убыванию.
	if !EpicLess(epic(1, 9, "APP-2", "backend"), epic(1, 2, "APP-1", "backend")) {
		t.Error("больший Priority должен идти раньше")
	}
	// Равный приоритет — по sequence_order.
	if !EpicLess(epic(1, 5, "APP-2", "backend"), epic(2, 5, "APP-1", "backend")) {
		t.Error("меньший sequence_order должен идти раньше")
	}
	// Равные приоритет и sequence_order — по task_id.
	if !EpicLess(epic(1, 5, "APP-1", "backend"), epic(1, 5, "APP-2", "backend")) {
		t.Error("меньший task_id должен идти раньше")
	}
}

// TestSortEpics: сортировка стабильна и даёт эффективный порядок очереди —
// фазы ровно по группам, внутри — приоритет по убыванию.
func TestSortEpics(t *testing.T) {
	epics := []*Epic{
		epic(1, 1, "TEST-1", "qa"),      // тест — последняя фаза
		epic(2, 5, "APP-2", "backend"),  // приложение, приоритет 5
		epic(1, 9, "APP-1", "frontend"), // приложение, приоритет 9
		epic(1, 3, "INFRA-1", "devops"), // инфраструктура — первая
		epic(1, 0, "APP-0", ""),         // без роли — приложение
	}
	SortEpics(epics)
	got := make([]string, 0, len(epics))
	for _, e := range epics {
		got = append(got, e.TaskID)
	}
	want := []string{"INFRA-1", "APP-1", "APP-2", "APP-0", "TEST-1"}
	if len(got) != len(want) {
		t.Fatalf("SortEpics длиной %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SortEpics = %v, want %v", got, want)
		}
	}
}

// TestReorderEpics: первый ID в заказе получает максимальный Priority; ревизию
// не трогает; неизвестный ID — ошибка; пропущенные эпики не меняются.
func TestReorderEpics(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, id := range []string{"EP-1", "EP-2", "EP-3"} {
		if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: id, Title: id}}); err != nil {
			t.Fatalf("CreateEpic(%s): %v", id, err)
		}
	}

	updated, err := s.ReorderEpics(ctx, []string{"EP-3", "EP-1", "EP-2"})
	if err != nil {
		t.Fatalf("ReorderEpics: %v", err)
	}
	if updated != 3 {
		t.Errorf("updated = %d, want 3", updated)
	}
	prios := map[string]int{}
	for _, id := range []string{"EP-1", "EP-2", "EP-3"} {
		e, _ := s.GetEpic(ctx, id)
		prios[id] = e.Priority
	}
	if prios["EP-3"] != 3 || prios["EP-1"] != 2 || prios["EP-2"] != 1 {
		t.Errorf("приоритеты = %v, want EP-3:3 EP-1:2 EP-2:1", prios)
	}

	// Повторный вызов с тем же порядком — ничего не обновляет.
	updated, err = s.ReorderEpics(ctx, []string{"EP-3", "EP-1", "EP-2"})
	if err != nil || updated != 0 {
		t.Errorf("идемпотентный повтор: updated=%d err=%v, want updated=0", updated, err)
	}

	// Неизвестный ID — ошибка; часть результата уже применена (EP-3 получил
	// приоритет 2 — первый элемент неудачного вызова), остальные не тронуты.
	if _, err := s.ReorderEpics(ctx, []string{"EP-3", "NO-SUCH"}); err == nil {
		t.Error("неизвестный ID должен давать ошибку")
	}
	e1, _ := s.GetEpic(ctx, "EP-1")
	e2, _ := s.GetEpic(ctx, "EP-2")
	e3, _ := s.GetEpic(ctx, "EP-3")
	if e1.Priority != 2 || e2.Priority != 1 || e3.Priority != 2 {
		t.Errorf("частичный сбой: EP-1=%d EP-2=%d EP-3=%d, want 2/1/2", e1.Priority, e2.Priority, e3.Priority)
	}

	// Пропущенные эпики сохраняют прежний приоритет: одиночный reorder
	// трогает только упомянутый ID.
	if _, err := s.ReorderEpics(ctx, []string{"EP-1"}); err != nil {
		t.Fatalf("reorder EP-1: %v", err)
	}
	e2, _ = s.GetEpic(ctx, "EP-2")
	e3, _ = s.GetEpic(ctx, "EP-3")
	if e2.Priority != 1 || e3.Priority != 2 {
		t.Errorf("пропущенные эпики изменены: EP-2=%d EP-3=%d", e2.Priority, e3.Priority)
	}

	// Ревизия не растёт — пересортировка не содержание.
	e, _ := s.GetEpic(ctx, "EP-3")
	if _, err := s.ReorderEpics(ctx, []string{"EP-1", "EP-2", "EP-3"}); err != nil {
		t.Fatalf("финальный reorder: %v", err)
	}
	eAfter, _ := s.GetEpic(ctx, "EP-3")
	if eAfter.Revision != e.Revision {
		t.Errorf("пересортировка изменила ревизию: %d -> %d", e.Revision, eAfter.Revision)
	}
}
