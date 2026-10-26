package usecases

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai/domain"
	"ai/domain/services"
)

// Ручной откат кода задачи (Ф-6, этап 3,
// PLAN-2026-10-05-todo-kanban-rollback.md).
//
// Логика вынесена из HTTP-слоя (Этап 6, «Интерфейсы и адаптеры»): server
// остаётся адаптером — декодирование запроса, вызов этого use-case и маппинг
// доменных ошибок в HTTP-коды. Откат делает только человек и только по явному
// подтверждению в UI: он необратим в рабочем дереве (коммиты остаются в reflog)
// и отбрасывает всё незакоммиченное. Автоматических откатов в системе нет
// намеренно (Р-1): заменой автооткату служит watchdog этапа 4 — смена модели и
// возобновление.

// Доменные ошибки отката — классификация для адаптера (HTTP-коды).
var (
	// ErrTaskNotFound — задачи с таким id нет на доске.
	ErrTaskNotFound = errors.New("задача не найдена")
	// ErrNoWorktree — у задачи нет рабочей копии (ветка не создавалась или уже
	// снята): откатывать нечего.
	ErrNoWorktree = errors.New("у задачи нет worktree: откатывать нечего")
	// ErrBadTarget — не указана или не распознана точка отката.
	ErrBadTarget = errors.New("некорректная цель отката")
	// ErrNoSuchCommit — целевого коммита не существует (опечатка в SHA,
	// «последняя рабочая версия» ещё не создана).
	ErrNoSuchCommit = errors.New("нет такого коммита")
)

// RollbackTarget переводит `to` из тела запроса в SHA. Именованные точки —
// last_good (последний раунд, прошедший проверку) и base (состояние на момент
// старта задачи); всё остальное считается SHA: произвольный откат к нужному
// коммиту должен быть возможен без правок кода.
func RollbackTarget(task *domain.Task, to string) (string, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return "", fmt.Errorf("%w: не указана точка отката (last_good, base или SHA)", ErrBadTarget)
	}
	cp := task.Checkpoint
	switch to {
	case "last_good":
		if cp == nil || strings.TrimSpace(cp.LastGoodSHA) == "" {
			return "", fmt.Errorf("%w: неизвестно последнее рабочее состояние (в задаче не было раунда, прошедшего проверку)", ErrBadTarget)
		}
		return cp.LastGoodSHA, nil
	case "base":
		if cp == nil || strings.TrimSpace(cp.BaseSHA) == "" {
			return "", fmt.Errorf("%w: неизвестна база задачи (worktree не создавался в этой сессии)", ErrBadTarget)
		}
		return cp.BaseSHA, nil
	}
	if strings.ContainsAny(to, " \t") {
		return "", fmt.Errorf("%w: %q", ErrBadTarget, to)
	}
	return to, nil
}

// WorktreeRollbacker откатывает рабочую копию задачи к коммиту target.
// Реализуется инфраструктурой (infrastructure/git + Executor); адаптер обязан
// переводить «коммита не существует» в ErrNoSuchCommit.
type WorktreeRollbacker interface {
	RollbackWorktree(ctx context.Context, worktree, target string) (sha string, submodules []string, err error)
}

// RollbackOutcome — итог отката для ответа API.
type RollbackOutcome struct {
	// SHA — коммит, к которому откатились.
	SHA string
	// Submodules — откатанные рекурсивно сабмодули.
	Submodules []string
	// Status — итоговый статус задачи (ready, если была в работе).
	Status domain.Status
}

// RollbackTask выполняет ручной откат кода задачи: откатывает рабочую копию к
// цели, переносит чекпойнт на откатанную точку и возвращает задачу в очередь,
// если она была в работе. worktree — уже разрешённый адаптером путь рабочей
// копии (пустой отклоняется как ErrNoWorktree до разбора цели).
func RollbackTask(ctx context.Context, store services.BoardStore, task *domain.Task, worktree, to string, rb WorktreeRollbacker) (RollbackOutcome, error) {
	if strings.TrimSpace(worktree) == "" {
		return RollbackOutcome{}, ErrNoWorktree
	}
	target, err := RollbackTarget(task, to)
	if err != nil {
		return RollbackOutcome{}, err
	}

	sha, submodules, err := rb.RollbackWorktree(ctx, worktree, target)
	if err != nil {
		if errors.Is(err, ErrNoSuchCommit) {
			return RollbackOutcome{}, fmt.Errorf("%w %s", ErrNoSuchCommit, target)
		}
		return RollbackOutcome{}, err
	}

	// Чекпойнт переезжает на откатанную точку: HEAD задачи теперь равен цели
	// отката, а отменённые раунды больше не являются ни «последним», ни
	// «последним рабочим». Ошибка проверки отменённого кода тоже снимается —
	// иначе UI продолжал бы показывать падение уже несуществующей версии.
	if err := store.PatchTask(ctx, task.TaskID, func(t *domain.Task) error {
		if t.Checkpoint == nil {
			t.Checkpoint = &domain.TaskCheckpoint{}
		}
		t.Checkpoint.LastSHA = sha
		t.Checkpoint.LastGoodSHA = sha
		t.LastError = ""
		t.AgentState = ""
		return nil
	}); err != nil {
		return RollbackOutcome{}, fmt.Errorf("обновление чекпойнта: %w", err)
	}

	// Задача возвращается в очередь: агент следующего прогона продолжит с
	// откатанного кода. Отмена/помощь человека/готово остаются без изменений
	// (иначе откат тихо воскресил бы отменённую задачу).
	status := task.Status
	if status == domain.StatusInfra || status == domain.StatusTestCases || status == domain.StatusDevelopment {
		// FSM запрещает переходы назад: обходим через PatchTask.
		if err := store.PatchTask(ctx, task.TaskID, func(cur *domain.Task) error {
			cur.Status = domain.StatusInfra
			return nil
		}); err != nil {
			return RollbackOutcome{}, fmt.Errorf("возврат в очередь: %w", err)
		}
		status = domain.StatusInfra
	}
	return RollbackOutcome{SHA: sha, Submodules: submodules, Status: status}, nil
}
