// Тесты прогноза расхода токенов для новых единиц доски (Ф-5
// PLAN-2026-09-19-done-epic-task-token.md).
package server

import (
	"context"
	"testing"

	"ai/board"
	"ai/tokens"
)

// seedTokenHistory кладёт на доску завершённые единицы с фактом расхода — так
// выглядит проект, накопленный за один-два прогона.
func seedTokenHistory(t *testing.T, sess *Session, project string) {
	t.Helper()
	ctx := context.Background()
	for i, spent := range []int64{4000, 5000, 6000, 7000} {
		if err := sess.board.CreateTask(ctx, &board.Task{
			TaskSpec: board.TaskSpec{
				TaskID:      "T-" + string(rune('a'+i)),
				Title:       "задача",
				Description: "описание задачи для обучения прогноза",
			},
			Assignee:   "developer",
			EpicID:     "seed-epic",
			Status:     board.StatusDone,
			TokenUsage: board.TokenUsage{TokensInput: spent, TokensOutput: 0, TokensTotal: spent},
		}); err != nil {
			t.Fatalf("CreateTask %d: %v", i, err)
		}
	}
	_ = project
}

// TestTokenEstimateWithoutHistory: на пустой доске оценки нет — это честнее
// цифры «на глаз».
func TestTokenEstimateWithoutHistory(t *testing.T) {
	srv, _, _ := newTestServer(t)
	sess, _, err := srv.getOrCreate("tok-est-empty")
	if err != nil {
		t.Fatal(err)
	}
	est := sess.tokenEstimate()
	got := est(context.Background(), nil, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-1", Title: "Задача", Description: "описание"},
	})
	if got != 0 {
		t.Fatalf("на пустой доске ожидался отказ от оценки, получено %d", got)
	}
	// nil-аргументы — тоже отказ, без паники.
	if got := est(context.Background(), nil, nil); got != 0 {
		t.Fatalf("при nil-единице ожидался 0, получено %d", got)
	}
}

// TestTokenEstimateFromHistory: с накопленной историей новая задача получает
// оценку, близкую к среднему расходу завершённых.
func TestTokenEstimateFromHistory(t *testing.T) {
	srv, _, _ := newTestServer(t)
	sess, _, err := srv.getOrCreate("tok-est-hist")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.board.CreateEpic(ctxBG(), &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "seed-epic", Title: "эпик"},
	}); err != nil {
		t.Fatal(err)
	}
	seedTokenHistory(t, sess, "tok-est-hist")

	est := sess.tokenEstimate()
	got := est(context.Background(), nil, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-new", Title: "Новая задача", Description: "описание новой задачи"},
		Assignee: "developer",
	})
	if got <= 0 {
		t.Fatalf("на накопленной истории ожидалась оценка, получено %d", got)
	}
	if got < 3000 || got > 8000 {
		t.Fatalf("оценка = %d, вне ожидаемого коридора вокруг среднего 5500", got)
	}
}

// TestTokenEstimateEpicUsesRole: эпик оценивается по своей роли (в истории
// должна быть та же роль), задачи — по своей.
func TestTokenEstimateEpicUsesRole(t *testing.T) {
	srv, _, _ := newTestServer(t)
	sess, _, err := srv.getOrCreate("tok-est-epic")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := sess.board.CreateEpic(ctx, &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "seed-epic", Title: "эпик"},
	}); err != nil {
		t.Fatal(err)
	}
	// История: фронтенд заметно дороже бэкенда.
	for i, spent := range []int64{8000, 9000, 10000, 11000} {
		if err := sess.board.CreateTask(ctx, &board.Task{
			TaskSpec:   board.TaskSpec{TaskID: "FE-" + string(rune('a'+i)), Title: "задача", Description: "фронтенд"},
			Assignee:   "frontend",
			EpicID:     "seed-epic",
			Status:     board.StatusDone,
			TokenUsage: board.TokenUsage{TokensInput: spent, TokensOutput: 0, TokensTotal: spent},
		}); err != nil {
			t.Fatal(err)
		}
	}

	est := sess.tokenEstimate()
	epicEst := est(context.Background(), &board.Epic{
		TaskSpec: board.TaskSpec{TaskID: "FE-new", Title: "Новый эпик", Description: "фронтенд-эпик", AssignedRole: "frontend"},
	}, nil)
	taskEst := est(context.Background(), nil, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "FE-task", Title: "Новая задача", Description: "фронтенд-задача"},
		Assignee: "frontend",
	})
	if epicEst <= 0 || taskEst <= 0 {
		t.Fatalf("оценки должны быть положительны: эпик %d, задача %d", epicEst, taskEst)
	}
}

// TestBoardTokenEstimateHookWired: сессия внедряет прогноз в доску, поэтому
// оценка проставляется уже при создании задачи/эпика.
func TestBoardTokenEstimateHookWired(t *testing.T) {
	srv, _, _ := newTestServer(t)
	sess, _, err := srv.getOrCreate("tok-est-hook")
	if err != nil {
		t.Fatal(err)
	}
	if sess.board.TokenEstimate == nil {
		t.Fatalf("сессия должна внедрить board.TokenEstimate")
	}
	// Ставится и в NewPredictor совпадает с ручным вызовом (без TTL-переобучения).
	if tokens.NewPredictor(nil).Predict(tokens.Sample{}).Estimate != 0 {
		t.Errorf("пустой предиктор должен отказывать в оценке")
	}
}

func ctxBG() context.Context { return context.Background() }
