// Порядок эпиков на доске и в очереди лидов: единый компаратор для Web UI
// (сортировка строк доски), оркестратора (выбор следующего эпика лидом) и
// ручной пересортировки (ReorderEpics). Держим его в одном месте, чтобы
// «что видно на доске» и «что берётся в работу» не расходились.
package domain

import (
	"sort"
	"strings"
)

// EpicPhase классифицирует эпик по фазе разработки: инфраструктура (DevOps) →
// приложение (backend/frontend) → тестирование (QA). Маркеры роли совпадают с
// isRole лидов (регистр не важен).
func EpicPhase(e *Epic) string {
	role := strings.ToLower(e.AssignedRole)
	has := func(markers ...string) bool {
		for _, m := range markers {
			if strings.Contains(role, m) {
				return true
			}
		}
		return false
	}
	switch {
	case has("qa", "тест", "testing"):
		return "test"
	case has("devops", "инфра", "infra", "sre", "docker", "k8s", "ci"):
		return "infra"
	default:
		return "app"
	}
}

// epicPhaseRank — приоритет фазы в очереди лидов: инфраструктура раньше
// приложения, тестирование (в т.ч. qaengineer) — после всей разработки.
func epicPhaseRank(phase string) int {
	switch phase {
	case "infra":
		return 0
	case "app":
		return 1
	default: // test
		return 2
	}
}

// EpicLess — порядок эпиков в очереди лидов и на доске:
//
//  1. фаза разработки (инфраструктура → приложение → тестирование, QA Last);
//  2. приоритет Priority по убыванию (чем выше строка на доске — тем больше
//     Priority);
//  3. sequence_order (порядок, заданный архитектором/лидом);
//  4. task_id — тайбрейкер для детерминизма.
//
// Фаза первична — ручная пересортировка меняет порядок только внутри фазы.
func EpicLess(a, b *Epic) bool {
	ar, br := epicPhaseRank(EpicPhase(a)), epicPhaseRank(EpicPhase(b))
	if ar != br {
		return ar < br
	}
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	ao, bo := a.SequenceOrder.Int(), b.SequenceOrder.Int()
	if ao != bo {
		return ao < bo
	}
	return a.TaskID < b.TaskID
}

// SortEpics сортирует срез эпиков в эффективном порядке очереди (EpicLess).
// Используется снимком доски: строки Web UI идут ровно в том порядке, в каком
// эпики берёт оркестратор.
func SortEpics(epics []*Epic) {
	sort.SliceStable(epics, func(i, j int) bool {
		return EpicLess(epics[i], epics[j])
	})
}

// BugLess — порядок багрепортов на доске:
//
//  1. заблокированные (нейрослоп) — всегда вниз: они не берутся в работу и не
//     должны забивать верх списка;
//  2. приоритет Priority по убыванию (чем выше — тем раньше баг в работе);
//  3. bug_id — тайбрейкер для детерминизма.
func BugLess(a, b *BugReport) bool {
	ab, bb := a.Status.Blocked(), b.Status.Blocked()
	if ab != bb {
		return !ab
	}
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	return a.BugID < b.BugID
}

// SortBugReports сортирует багрепорты в порядке BugLess. Используется снимком
// доски, чтобы строки багов шли от важных к заблокированным.
func SortBugReports(bugs []*BugReport) {
	sort.SliceStable(bugs, func(i, j int) bool {
		return BugLess(bugs[i], bugs[j])
	})
}
