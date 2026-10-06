package server

// sandbox.go — сессионная песочница проекта (Этап 1, п. 1.5, 1.7).
//
// Контейнер поднимается на каждый запуск оркестрации (sess.start) и снимается,
// когда раннер завершился (defer в горутине-раннере). При
// CODEGEN_SANDBOX=session отсутствие контейнера — это ошибка старта, а не
// повод тихо выполнить задачу на хосте.
//
// Что монтируется. В контейнер идёт РОДИТЕЛЬСКИЙ каталог корня проекта:
// worktree'и задач (.wt-task-<имя>-…, .resolve-…, .conflict-…) создаются
// соседями клона, и корень alone их не покрыл бы. Для temp-проектов родитель —
// общий temp/, куда входят и другие проекты: смешивания нет, потому что
// матчинг каталогов внутри контейнера идёт по реестру (sandbox.coverLen —
// каталог проекта или его dot-сосед с именем проекта).
//
// Образ и сеть выбираются в sandbox.LoadConfig (пустая сеть для сессии по
// умолчанию, whitelist при заданном CODEGEN_SANDBOX_ALLOW_DOMAINS).

import (
	"context"
	"path/filepath"
	"time"

	"ai/logging"
	"ai/projects"
	"ai/sandbox"
)

// sandboxStartTimeout — бюджет старта сессии: сюда входят pull образа и
// создание сети/прокси. Считаем от снимка оркестрации (WithoutCancel): стоп
// пользователя не должен оставить половину контейнера.
const sandboxStartTimeout = 60 * time.Second

// startProjectSandbox поднимает сессионный контейнер проекта. No-op, если
// CODEGEN_SANDBOX не session. Любая другая ошибка — отказ запуска оркестрации.
func (s *Server) startProjectSandbox(ctx context.Context, project string) error {
	cfg := sandbox.LoadConfig()
	if cfg.Mode != sandbox.ModeSession {
		return nil
	}
	root := projects.ProjectDir(project)
	if inf, err := s.reg.Get(project); err == nil {
		root = inf.Root
	}
	// Родительский каталог (worktree — сосед корня); на самом корне ФС
	// монтировать нечего и не нужно.
	mounts := []string{root}
	if parent := filepath.Dir(root); parent != root {
		mounts = []string{parent}
	}

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sandboxStartTimeout)
	defer cancel()
	// StartSession сам регистрирует Workspace в sandbox-реестре: FileOps.Run
	// и приёмка находят его по имени проекта/пути каталога.
	if _, err := sandbox.StartSession(sctx, sandbox.SessionOptions{
		Project: project,
		Dir:     root,
		Mounts:  mounts,
		Config:  cfg,
	}); err != nil {
		return errf("песочница сессии проекта %s не поднялась: %v", project, err)
	}
	return nil
}

// stopProjectSandbox снимает сессионный контейнер проекта. No-op без сессии.
// Запускается в defer горутины-раннера: даже при отмене контекста остановки
// (пользователь нажал «Стоп») контейнер убирается штатно.
func (s *Server) stopProjectSandbox(project string) {
	if err := sandbox.StopSession(context.Background(), project); err != nil {
		logging.For(project).Warnf("[sandbox] контейнер сессии не снят: %v", err)
	}
}
