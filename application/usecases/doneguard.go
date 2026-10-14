package usecases

// Гард «phantom done» (Ф-6, инцидент mytrip/FEL-04).
//
// Что произошло: задача FEL-04 ни разу не запускала разработчика по делу,
// её ветка осталась на коммите предыдущей задачи, worktree был чистым — но
// fallback оркестратора («в работе → выполнена (агент не сменил статус)»)
// перевёл её в done. В итоге доска показывала «выполнено», MR был пустым,
// а эпик ждал работу, которой не было. Статус done терминальный: откатить
// такой финал без ручного вмешательства нельзя, поэтому проверять надо ДО
// перехода.
//
// Правило: задача git-проекта не может стать done, пока в её ветке нет
// ни одного своего коммита относительно базы эпика И worktree не содержит
// незакоммиченных правок. Любой из двух признаков означает, что работа была.
// Ошибки git считаем «нет доказательств» только когда ветка существует;
// если ветки нет (задача без git-flow) — проверка не применяется.
//
// Логика вынесена из HTTP-слоя (Этап 6, «Интерфейсы и адаптеры»): server
// остаётся адаптером — реализует DoneGuardSource поверх реестра и git.Repo,
// а решение «можно ли закрыть задачу» принимается здесь.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"ai/domain"
)

// DoneGuardSource — окружение гарда: реестр проектов, ветки задач/эпиков и
// состояние рабочей копии. Реализуется инфраструктурой (server + git.Repo).
type DoneGuardSource interface {
	// IsGitProject — проект существует и ведётся как git-проект. Для не-git
	// проекта (kind=dir) гард молчит: там нет ни веток, ни worktree.
	IsGitProject(project string) bool
	// TaskBranch — ветка задачи; ok=false, если ветки нет (задача закрывается
	// без git-flow — аналитика, решение) или её имя пусто.
	TaskBranch(project, taskID string) (branch string, ok bool)
	// EpicBase — база для сравнения коммитов: ветка эпика, если она есть,
	// иначе git_base проекта.
	EpicBase(project, epicID string) string
	// CommitsAhead — число своих коммитов ветки branch относительно base.
	// Ошибка означает «не смогли доказать» — гард пропускает переход.
	CommitsAhead(ctx context.Context, project, base, branch string) (int, error)
	// DirtyPaths — незакоммиченные пути в worktree задачи. Ошибка или
	// отсутствие worktree означает «нет доказательств правок».
	DirtyPaths(ctx context.Context, project, taskID string) ([]string, error)
	// Warn логирует недоказанную проверку (гард не блокирует переход).
	Warn(project, format string, args ...any)
}

// orchestratorGitignore — служебный .gitignore, который кладёт сам
// оркестратор при создании worktree задачи (см. server.ensureTaskGitignore).
const orchestratorGitignore = ".gitignore"

// GuardTaskDone — проверка перед переводом задачи в done. enabled=false
// (KANBAN_DONE_REQUIRES_COMMIT=0) выключает гард для досок, где задачи
// закрываются без кода.
func GuardTaskDone(ctx context.Context, src DoneGuardSource, enabled bool, project string, t *domain.Task) error {
	if t == nil || t.TaskID == "" || !enabled {
		return nil
	}
	if !src.IsGitProject(project) {
		return nil
	}
	branch, ok := src.TaskBranch(project, t.TaskID)
	if !ok {
		// Ветки задачи нет — не наша забота: done у задачи без ветки
		// легально (аналитика, решение), гард не блокирует.
		return nil
	}
	base := src.EpicBase(project, t.EpicID)

	// Признак 1: в worktree есть незакоммиченные правки агента — работа велась,
	// авто-коммит при done её зафиксирует. Служебные файлы оркестратора
	// (.gitignore, который сам пишет ensureTaskGitignore) доказательством
	// работы не считаются, иначе гард был бы слепым.
	if paths, err := src.DirtyPaths(ctx, project, t.TaskID); err == nil {
		for _, p := range paths {
			if !IsOrchestratorArtifact(p) {
				return nil
			}
		}
	}

	// Признак 2: ветка задачи ушла от базы эпика.
	n, err := src.CommitsAhead(ctx, project, base, branch)
	if err != nil {
		// Не смогли доказать, что работа была, — но и не смогли доказать,
		// что её не было: блокировать переход человека опасно, логируем.
		src.Warn(project, "[гард done] задача %s: не удалось посчитать коммиты (%s..%s): %v — переход разрешён",
			t.TaskID, base, branch, err)
		return nil
	}
	if n > 0 {
		return nil
	}
	return fmt.Errorf("задача %s не может стать выполненной: в ветке %s нет ни одного коммита относительно %s, и worktree пуст — работа фактически не сделана (инцидент FEL-04). Запусти задачу в работу или закрой её как отменённую; если задача закрывается без кода — KANBAN_DONE_REQUIRES_COMMIT=0",
		t.TaskID, branch, base)
}

// IsOrchestratorArtifact — служебные пути, которые кладёт сам оркестратор.
// Неизменённый .gitignore, добавленный при создании worktree задачи, не должен
// превращать пустую задачу в «выполненную» (инцидент FEL-04).
func IsOrchestratorArtifact(path string) bool {
	p := filepath.ToSlash(strings.TrimSpace(path))
	if p == "" {
		return true
	}
	return p == orchestratorGitignore || strings.HasSuffix(p, "/"+orchestratorGitignore)
}
