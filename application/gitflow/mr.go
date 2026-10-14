package gitflow

// MR/PR в git-workflow (Ф-5): политика открытия merge request'а.
//
// Правило: MR задачи открывается только когда в её ветке есть свои коммиты
// относительно базы (иначе фордж отвечает 422 «No commits between …»),
// база MR существует в remote (GitHub/GitLab отклоняют MR с неизвестной
// base), ветка запушена, а созданный MR записан в side-реестр (источник
// правды о существовании MR переживает перезапуск). Операции git и форджа
// подключаются портами; HTTP-контекст остаётся в `server`.

import (
	"context"
	"errors"
	"fmt"
)

// ErrEmptyBranch — MR создавать нечего: в ветке нет коммитов относительно
// базы MR. Это состояние, а не сбой сервера: ручной путь отвечает 400 с
// понятным текстом, авто-шаг тихо пропускает.
var ErrEmptyBranch = errors.New("в ветке нет коммитов относительно базы MR — создавать MR нечего")

// MR — зарегистрированный merge request (side-реестр workspace).
type MR struct {
	URL    string
	Source string
	Target string
	State  string
}

// MROptions — параметры запроса MR форджу.
type MROptions struct {
	SourceBranch string
	TargetBranch string
	Title        string
	Description  string
}

// MRGateway — git-операции, нужные для открытия MR.
type MRGateway interface {
	// CountCommits — число коммитов ветки branch относительно base.
	CountCommits(ctx context.Context, base, branch string) (int, error)
	// EnsureRemoteBranch — база MR существует в remote (если ветка живёт
	// только в локальном клоне, она пушится).
	EnsureRemoteBranch(ctx context.Context, base string) error
	// PushBranch — пушит ветку в remote.
	PushBranch(ctx context.Context, branch, base string) error
}

// Forge — создание MR в хостинге (GitHub/GitLab).
type Forge interface {
	CreateMergeRequest(ctx context.Context, opts MROptions) (string, error)
}

// MRRegistry — запись MR в side-реестр проекта.
type MRRegistry interface {
	SetTaskMR(taskID string, mr MR) error
	SetEpicMR(epicID string, mr MR) error
}

// MRCreator — use-case открытия MR задачи → ветку эпика.
type MRCreator struct {
	Gateway  MRGateway
	Forge    Forge
	Registry MRRegistry
}

// CreateTaskMR открывает MR задачи. Вызывающий держит mergeLock проекта:
// проверяется наличие коммитов, база MR появляется в remote, ветка пушится,
// MR создаётся и записывается в реестр.
//
// Ветка без своих коммитов — ErrEmptyBranch (проверяется ДО push, чтобы не
// толкать ветку впустую). Ошибка самого git при подсчёте коммитов проверку НЕ
// проваливает: дефекты git не должны блокировать создание MR.
func (c MRCreator) CreateTaskMR(ctx context.Context, taskID, title, description, source, target string) (string, error) {
	if ok, err := c.branchHasCommits(ctx, target, source); err == nil && !ok {
		return "", fmt.Errorf("%w: %s → %s", ErrEmptyBranch, source, target)
	}
	// База MR (ветка эпика) должна существовать в remote: GitHub/GitLab
	// отклоняют MR с неизвестной base (422 "base invalid"), а ветка эпика
	// до первого MR задачи живёт только в локальном клоне.
	if err := c.Gateway.EnsureRemoteBranch(ctx, target); err != nil {
		return "", fmt.Errorf("подготовка базы MR: %w", err)
	}
	if err := c.Gateway.PushBranch(ctx, source, target); err != nil {
		return "", fmt.Errorf("push ветки %s: %w", source, err)
	}
	mrURL, err := c.Forge.CreateMergeRequest(ctx, MROptions{
		SourceBranch: source,
		TargetBranch: target,
		Title:        title,
		Description:  description,
	})
	if err != nil {
		return "", fmt.Errorf("создание MR задачи: %w", err)
	}
	if err := c.Registry.SetTaskMR(taskID, MR{URL: mrURL, Source: source, Target: target, State: "open"}); err != nil {
		return "", fmt.Errorf("ошибка записи MR в реестр: %w", err)
	}
	return mrURL, nil
}

// CreateEpicMR открывает MR релизной ветки эпика → базу (обычно main).
// Вызывающий держит mergeLock проекта: ветка эпика пушится, MR создаётся и
// записывается в реестр. Ветка без коммитов — ErrEmptyBranch (та же защита,
// что у задачи).
func (c MRCreator) CreateEpicMR(ctx context.Context, epicID, title, description, source, target string) (string, error) {
	if ok, err := c.branchHasCommits(ctx, target, source); err == nil && !ok {
		return "", fmt.Errorf("%w: %s → %s", ErrEmptyBranch, source, target)
	}
	if err := c.Gateway.PushBranch(ctx, source, target); err != nil {
		return "", fmt.Errorf("push ветки %s: %w", source, err)
	}
	mrURL, err := c.Forge.CreateMergeRequest(ctx, MROptions{
		SourceBranch: source,
		TargetBranch: target,
		Title:        title,
		Description:  description,
	})
	if err != nil {
		return "", fmt.Errorf("создание MR эпика: %w", err)
	}
	if err := c.Registry.SetEpicMR(epicID, MR{URL: mrURL, Source: source, Target: target, State: "open"}); err != nil {
		return "", fmt.Errorf("ошибка записи MR в реестр: %w", err)
	}
	return mrURL, nil
}

// branchHasCommits — есть ли в ветке свои коммиты. Ошибка git возвращается
// отдельно и не трактуется как «коммитов нет».
func (c MRCreator) branchHasCommits(ctx context.Context, base, branch string) (bool, error) {
	n, err := c.Gateway.CountCommits(ctx, base, branch)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
