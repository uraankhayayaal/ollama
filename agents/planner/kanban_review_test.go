package planner

import (
	"context"
	"os"
	"strings"
	"testing"

	"ai/agents"
	"ai/agents/architect"
	"ai/board"
	"ai/projects"
	"ai/runner"

	"github.com/alicebob/miniredis/v2"
)

// reviewFlowProvider — фейковый провайдер фазы ревизии (Ф-8): если модель
// вызвана как архитектор в режиме ревизии (ReviewerMode), возвращает успешный
// ответ и запоминает список черновиков из задания (GetUserMessages). В
// остальных ролях делегирует декапозицию/исполнение как kanbanProvider.
type reviewFlowProvider struct {
	reviewerCalls int
	draftsLog     []string
}

func (p *reviewFlowProvider) Generate(_ context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	if a, ok := agent.(*architect.Architect); ok && a.ReviewerMode {
		p.reviewerCalls++
		if us := agent.GetUserMessages(); len(us) > 0 {
			p.draftsLog = append(p.draftsLog, us[0].Message)
		}
		return &runner.AgentResponse{Content: "Ревизия проведена."}, nil
	}
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	switch {
	case strings.HasPrefix(umsg, "Декомпозируй эпик"):
		return &runner.AgentResponse{Content: leadDecompositionJSON}, nil
	case strings.HasPrefix(umsg, "Ты — специалист"):
		return &runner.AgentResponse{Content: "Задача выполнена."}, nil
	}
	return &runner.AgentResponse{Content: ""}, nil
}

// newReviewRunner создаёт KanbanRunner с reviewFlowProvider поверх in-memory
// Redis для инжекции/проверки фазы ревизии во всех режимах.
func newReviewRunner(t *testing.T, project string) (*KanbanRunner, *board.Store, *reviewFlowProvider) {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: project})
	t.Cleanup(func() { _ = os.RemoveAll(projects.ProjectDir(project)) })
	p := &reviewFlowProvider{}
	return NewKanbanRunner(p, store), store, p
}

// TestArchitectReviewRunsInBoardOnlyAndNormal — Ф-8: фаза phaseArchitectReview
// исполняется и в board-only, и в обычном режиме. Черновик (RequiresReview=true)
// ревьюится, флаг снимается, LeadSyncedRev синхронизируется — декомпозиция
// лидом становится возможной независимо от режима запуска доски.
func TestArchitectReviewRunsInBoardOnlyAndNormal(t *testing.T) {
	for _, mode := range []struct {
		name      string
		boardOnly bool
	}{
		{"обычный режим", false},
		{"board-only", true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			ctx := context.Background()
			kr, store, p := newReviewRunner(t, "kanban-review-mode")

			if mode.boardOnly {
				kr.SetBoardOnly(true)
			}
			// Лид направлений утверждён; черновик из такого эпика — чат дал только
			// task_id/title/description, лида назначит архитектор при ревизии.
			if err := store.CreateEpic(ctx, &board.Epic{
				TaskSpec: board.TaskSpec{
					TaskID: "CHAT-01", Title: "Черновик из чата", Description: "Черновик ТЗ", SequenceOrder: 1,
				},
				RequiresReview: true,
			}); err != nil {
				t.Fatal(err)
			}

			progress, err := kr.phaseArchitectReview(ctx)
			if err != nil {
				t.Fatalf("фаза ревизии (boardOnly=%v): %v", mode.boardOnly, err)
			}
			if !progress {
				t.Fatal("фаза ревизии должна вернуть прогресс: черновик сревизован")
			}
			if p.reviewerCalls != 1 {
				t.Fatalf("ревьюер вызван %d раз (boardOnly=%v), ожидался 1", p.reviewerCalls, mode.boardOnly)
			}

			epic, err := store.GetEpic(ctx, "CHAT-01")
			if err != nil {
				t.Fatal(err)
			}
			if epic.RequiresReview {
				t.Fatal("после ревизии флаг RequiresReview должен быть снят")
			}
			if epic.LeadSyncedRev != epic.Revision {
				t.Fatalf("LeadSyncedRev = %d, ревизия = %d: не синхронизировано после ревизии", epic.LeadSyncedRev, epic.Revision)
			}

			// Утверждённый черновик снова виден лиду — декомпозиция возможна.
			epics, err := store.ListEpics(ctx)
			if err != nil {
				t.Fatal(err)
			}
			next, needDecompose, _, err := kr.nextLeadEpic(epics)
			if err != nil {
				t.Fatal(err)
			}
			if next == nil || next.TaskID != "CHAT-01" || !needDecompose {
				t.Fatalf("после ревизии лид должен взять CHAT-01 на декомпозицию (boardOnly=%v), got %+v", mode.boardOnly, next)
			}
		})
	}
}

// TestArchitectBacklogNotReviewed — Ф-8: собственный бэклог архитектора
// (RequiresReview=false из submit_architecture_backlog) в фазу ревизии НЕ
// попадает — свой план архитектор не ревьюит собой. Ревьюится только
// черновик чата (RequiresReview=true).
func TestArchitectBacklogNotReviewed(t *testing.T) {
	ctx := context.Background()
	kr, store, p := newReviewRunner(t, "kanban-review-backlog")

	// Бэклог архитектора (как создаёт submit_architecture_backlog): явно
	// RequiresReview=false — ревизии не требует.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{
			TaskID: "ARCH-01", Title: "Бэкенд-сервис", Description: "Архитектурный план", AssignedRole: "Backend Lead", SequenceOrder: 1,
		},
		RequiresReview: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{
			TaskID: "ARCH-02", Title: "Инфраструктура", Description: "Инфра", AssignedRole: "DevOps Lead", SequenceOrder: 2,
		},
		RequiresReview: false,
	}); err != nil {
		t.Fatal(err)
	}

	progress, err := kr.phaseArchitectReview(ctx)
	if err != nil {
		t.Fatalf("фаза ревизии: %v", err)
	}
	if progress {
		t.Fatal("бэклог архитектора не должен требовать ревизии (свой план не ревьюится собой)")
	}
	if p.reviewerCalls != 0 {
		t.Fatalf("ревьюер вызван %d раз: бэклог без RequiresReview не должен попадать в ревизию", p.reviewerCalls)
	}

	// А вот черновик чата в бэклоге — ревизуется; бэклог архитектора остаётся
	// не тронутым (в задание ревизора не попадает).
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{
			TaskID: "CHAT-01", Title: "Черновик из чата", Description: "Черновик", SequenceOrder: 3,
		},
		RequiresReview: true,
	}); err != nil {
		t.Fatal(err)
	}
	if progress, err = kr.phaseArchitectReview(ctx); err != nil {
		t.Fatalf("фаза ревизии с черновиком: %v", err)
	}
	if !progress {
		t.Fatal("черновик чата должен сревизоваться")
	}
	if p.reviewerCalls != 1 {
		t.Fatalf("ревьюер вызван %d раз, ожидался 1", p.reviewerCalls)
	}
	if len(p.draftsLog) != 1 {
		t.Fatalf("заданий ревизора %d, ожидалось 1", len(p.draftsLog))
	}
	if strings.Contains(p.draftsLog[0], "ARCH-01") || strings.Contains(p.draftsLog[0], "ARCH-02") {
		t.Fatalf("задание ревизора не должно содержать эпики бэклога архитектора:\n%s", p.draftsLog[0])
	}
	if !strings.Contains(p.draftsLog[0], "CHAT-01") {
		t.Fatalf("задание ревизора должно содержать черновик чата CHAT-01:\n%s", p.draftsLog[0])
	}
}

// TestChatEpicRequiresReviewDefersDecomposition — Ф-8: эпик-черновик из чата
// (RequiresReview=true) не выдаётся лиду на декомпозицию, пока Системный
// архитектор не провёл ревизию и не снял флаг. До ревизии лид пропускает
// его (на декомпозицию никого не берёт — черновик вне очереди); после
// ревизии черновик становится обычным эпиком и выдаётся лиду.
func TestChatEpicRequiresReviewDefersDecomposition(t *testing.T) {
	ctx := context.Background()
	kr, store := newKanbanRunner(t)

	// E-1 — уже утверждённый эпик (задача готова, ревизия лида совпадает с
	// ревизией эпика) — лиду его декомпозировать не нужно.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "E-1", Title: "Обычный эпик", AssignedRole: "Backend Lead", SequenceOrder: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-1", Title: "Задача", AssignedRole: "Go Developer", SequenceOrder: 1},
		EpicID:   "E-1",
	}); err != nil {
		t.Fatal(err)
	}
	for _, st := range []board.Status{board.StatusAnalysis, board.StatusReady} {
		if err := store.SetTaskStatus(ctx, "T-1", st); err != nil {
			t.Fatal(err)
		}
	}
	if epic, err := store.GetEpic(ctx, "E-1"); err != nil {
		t.Fatal(err)
	} else {
		epic.LeadSyncedRev = epic.Revision
		if err := store.SaveEpic(ctx, epic); err != nil {
			t.Fatal(err)
		}
	}

	// E-2 — черновик из чата (RequiresReview=true): создан сабмитом в чате,
	// ждёт ревизии архитектора.
	if err := store.CreateEpic(ctx, &board.Epic{
		TaskSpec:       board.TaskSpec{TaskID: "E-2", Title: "Черновик из чата", AssignedRole: "Backend Lead", SequenceOrder: 2},
		RequiresReview: true,
	}); err != nil {
		t.Fatal(err)
	}

	epics, err := store.ListEpics(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// До ревизии черновик пропускается: лид не видит E-2 (очередь пуста для
	// декомпозиции — E-1 уже сработан).
	epic, needDecompose, _, err := kr.nextLeadEpic(epics)
	if err != nil {
		t.Fatal(err)
	}
	if epic != nil {
		t.Fatalf("до ревизии лид не должен брать черновик E-2, got %+v", epic)
	}
	if needDecompose {
		t.Fatal("черновик не требует декомпозиции до ревизии")
	}

	// Ревизия архитектора прошла: снимаем флаг (в реальности это делает
	// phaseArchitectReview) — теперь черновик виден лиду как обычный эпик.
	epic2, err := store.GetEpic(ctx, "E-2")
	if err != nil {
		t.Fatal(err)
	}
	epic2.RequiresReview = false
	if err := store.SaveEpic(ctx, epic2); err != nil {
		t.Fatal(err)
	}

	epics, err = store.ListEpics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	epic, needDecompose, _, err = kr.nextLeadEpic(epics)
	if err != nil {
		t.Fatal(err)
	}
	if epic == nil || epic.TaskID != "E-2" {
		t.Fatalf("после ревизии лид должен взять E-2 (черновик утверждён), got %+v", epic)
	}
	if !needDecompose {
		t.Fatal("утверждённый черновик требует декомпозиции")
	}
}
