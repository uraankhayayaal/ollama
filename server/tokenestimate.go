// Прогноз расхода токенов для новых единиц доски (Ф-5
// PLAN-2026-09-19-done-epic-task-token.md) — тонкий адаптер ресурсов сессии к
// политике application/usecases (Этап 6, «Интерфейсы и адаптеры»): логика
// обучения и оценки живёт в usecases.NewTokenEstimator.
package server

import (
	"context"

	"ai/application/usecases"
	"ai/domain"
)

// tokenEstimate возвращает функцию оценки расхода токенов для доски проекта
// (совместима с board.Store.TokenEstimate): обучение — по истории доски
// (usecases.NewTokenEstimator), источник и лог — ресурсы сессии.
func (sess *Session) tokenEstimate() func(context.Context, *domain.Epic, *domain.Task) int64 {
	return usecases.NewTokenEstimator(sess.board, sess.log)
}
