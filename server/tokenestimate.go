// Прогноз расхода токенов для новых единиц доски (Ф-5
// PLAN-2026-09-19-done-epic-task-token.md).
//
// Прогноз ставится в момент создания задачи (лидом направления) или эпика
// (архитектором) и хранится рядом с фактом — чтобы по доске было видно, насколько
// прогноз совпал с реальностью. Обучающая выборка — завершённые единицы этой
// же доски с записанным фактом (tokens.HistoryFromBoard).
//
// Модель переобучается не чаще раза в tokenEstimateTTL: создание задач идёт
// десятками за прогон, а перечитывание доски на каждый вызов — лишние запросы
// в Redis. Пока истории нет (первые единицы проекта), оценка не выдаётся —
// это честнее, чем цифра «на глаз».
package server

import (
	"context"
	"sync"
	"time"

	"ai/board"
	"ai/tokens"
)

// tokenEstimateTTL — минимальный интервал между переобучениями предиктора.
const tokenEstimateTTL = time.Minute

// tokenEstimate возвращает функцию оценки расхода токенов для доски проекта
// (совместима с board.Store.TokenEstimate). nil-safe: без доски/Redis ошибки
// не роняют создание единицы, оценка просто не ставится.
func (sess *Session) tokenEstimate() func(context.Context, *board.Epic, *board.Task) int64 {
	var (
		mu      sync.Mutex
		pred    *tokens.Predictor
		trained time.Time
	)
	return func(ctx context.Context, e *board.Epic, t *board.Task) int64 {
		mu.Lock()
		defer mu.Unlock()
		if pred == nil || time.Since(trained) >= tokenEstimateTTL {
			samples, err := tokens.HistoryFromBoard(ctx, sess.board)
			if err != nil {
				sess.log.Warnf("прогноз токенов: чтение истории: %v", err)
				return 0
			}
			pred = tokens.NewPredictor(samples)
			trained = time.Now()
		}
		var sample tokens.Sample
		switch {
		case t != nil:
			sample = tokens.SampleFromTask(t)
		case e != nil:
			sample = tokens.SampleFromEpic(e, len(e.Tasks))
		default:
			return 0
		}
		est := pred.Predict(sample)
		if est.Basis == tokens.BasisNone {
			return 0
		}
		if est.Basis == tokens.BasisModel {
			sess.log.Detailf("прогноз токенов: %s ≈ %d (%s, ошибка модели %.0f%%)",
				sample.Role, est.Estimate, est.Basis, est.MAE)
		}
		return est.Estimate
	}
}
