package usecases

import (
	"context"
	"sync"
	"time"

	"ai/domain"
	"ai/shared/tokens"
)

// Прогноз расхода токенов для новых единиц доски (Ф-5
// PLAN-2026-09-19-done-epic-task-token.md).
//
// Прогноз ставится в момент создания задачи (лидом направления) или эпика
// (архитектором) и хранится рядом с фактом — чтобы по доске было видно, насколько
// прогноз совпал с реальностью. Обучающая выборка — завершённые единицы этой же
// доски с записанным фактом (tokens.HistoryFromBoard).
//
// Политика (ленивое обучение предиктора с TTL) вынесена из HTTP-слоя (Этап 6):
// server оставляет только адаптер ресурсов сессии.

// TokenEstimateTTL — минимальный интервал между переобучениями предиктора.
// Создание задач идёт десятками за прогон, а перечитывание доски на каждый
// вызов — лишние запросы в Redis.
const TokenEstimateTTL = time.Minute

// TokenEstimateLogger — минимальный лог для прогноза токенов (реализуется
// logging.Logger). Может быть nil.
type TokenEstimateLogger interface {
	Warnf(format string, args ...any)
	Detailf(format string, args ...any)
}

// NewTokenEstimator возвращает функцию оценки расхода токенов для доски проекта
// (совместима с board.Store.TokenEstimate). nil-safe: без истории/Redis ошибки
// не роняют создание единицы, оценка просто не ставится. Пока истории нет
// (первые единицы проекта), оценка не выдаётся — это честнее цифры «на глаз».
func NewTokenEstimator(store tokens.HistoryStore, log TokenEstimateLogger) func(context.Context, *domain.Epic, *domain.Task) int64 {
	var (
		mu      sync.Mutex
		pred    *tokens.Predictor
		trained time.Time
	)
	return func(ctx context.Context, e *domain.Epic, t *domain.Task) int64 {
		mu.Lock()
		defer mu.Unlock()
		if pred == nil || time.Since(trained) >= TokenEstimateTTL {
			samples, err := tokens.HistoryFromBoard(ctx, store)
			if err != nil {
				if log != nil {
					log.Warnf("прогноз токенов: чтение истории: %v", err)
				}
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
		if est.Basis == tokens.BasisModel && log != nil {
			log.Detailf("прогноз токенов: %s ≈ %d (%s, ошибка модели %.0f%%)",
				sample.Role, est.Estimate, est.Basis, est.MAE)
		}
		return est.Estimate
	}
}
