// Порядок эпиков на доске и в очереди лидов. Реализация принадлежит домену
// (domain/order.go); здесь — обёртки для совместимости кода доски и server.
package board

import "ai/domain"

// EpicPhase классифицирует эпик по фазе разработки (см. domain.EpicPhase).
func EpicPhase(e *Epic) string { return domain.EpicPhase(e) }

// EpicLess — порядок эпиков в очереди лидов и на доске (см. domain.EpicLess).
func EpicLess(a, b *Epic) bool { return domain.EpicLess(a, b) }

// SortEpics сортирует срез эпиков в порядке очереди (см. domain.SortEpics).
func SortEpics(epics []*Epic) { domain.SortEpics(epics) }
