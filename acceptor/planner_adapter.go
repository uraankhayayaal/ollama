package acceptor

import "ai/domain"

// PlannerAdapter — адаптер детерминированной приёмки под порт
// application/planner.Acceptor. Инфраструктура сама читает ACCEPT_*-конфиг и
// запускает сборку/проверки; application-слой зависит только от отчёта
// domain.Report.
type PlannerAdapter struct{}

// MaxRounds возвращает бюджет циклов «приёмка → исправления → приёмка».
func (PlannerAdapter) MaxRounds() int { return LoadConfig().MaxRounds }

// Accept выполняет приёмку директории с актуальным ACCEPT_*-конфигурацией.
func (PlannerAdapter) Accept(dir string) *domain.Report {
	return Accept(dir, LoadConfig())
}
