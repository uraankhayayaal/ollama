// Индексация RAG по содержимому ветки Git и её поддержание «свежей»
// (см. docs/plans/PLAN-2026-09-28-done-rag-main-freshness.md).
//
// Зачем: версионированный индекс (PLAN-2026-09-27) предполагает, что в Qdrant
// есть актуальные точки ветки `main` — именно они попадают в выдачу CodeSearch
// вместе с веткой агента (rag/search.go, activeBranchCond). Но:
//
//   - ни один merge в main (релиз эпика, авто-мёрдж) не переиндексировал main,
//     поэтому после релиза эпика его код в индексе отсутствовал;
//   - фоновая индексация обходит РАБОЧУЮ КОПИЮ проекта (temp/<имя>), которая
//     стоит на ветке агента (ai/<имя>) и никогда не подтягивается из main;
//     подписывать такой снимок как main нельзя — это чужой код под чужим ярлыком;
//   - рабочая копия (её читают архитектор, лиды и баг-эксперт в статусе
//     «анализ») сама не обновлялась из main, поэтому смена эпика показывала
//     код до релизов предыдущих эпиков.
//
// Решение: индекс ветки строится из git-объекта по ref (`git ls-tree` +
// `git show`), а рабочая копия агентов синхронизируется с main. Обе операции
// фоновые и не блокируют git-flow: ошибки — только в лог.

package server

import (
	"context"
	"fmt"
	"strings"

	apprag "ai/application/rag"
	"ai/infrastructure/git"
	"ai/infrastructure/rag"
	"ai/logging"
	"ai/workspace"
	"time"
)

// loadRefItems — текущий загрузчик снапшота ветки (пакетная переменная для
// hermetic-тестов; порт apprag.RefLoader).
var loadRefItems apprag.RefLoader = apprag.LoadRefItems

// IndexRefBackground индексирует содержимое ветки проекта из git (по ref), а не
// рабочую копию. Основной сценарий — главная ветка после merge эпика: без
// переиндексации main агенты следующих эпиков ищут по коду до релизов
// предыдущих (ветка main в индексе просто пуста).
//
// Если активные точки ветки на этом коммите уже есть (main не менялся с
// прошлого прогона) — индексация пропускается: полный прогон стоит эмбеддингов
// всех файлов проекта, а хук срабатывает на каждый старт задачи.
func (sess *Session) IndexRefBackground(ctx context.Context, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("не задана ветка для индексации")
	}
	dir := sess.projectDir()
	ex := sess.srv.gitExec
	job := apprag.RefJob{Project: sess.project, Dir: dir, Exec: ex, Loader: loadRefItems, Sink: ragIndexSink{sess: sess}}
	return sess.startIndexBackground("ветка "+ref, ref,
		func(bgctx context.Context, indexer apprag.Indexer) (*rag.IndexResult, string, bool, error) {
			return job.Run(bgctx, indexer, ref)
		})
}

// IndexRefDiffBackground индексирует в ветке ref только файлы, которых нет в
// базовой ветке base (`git diff base...ref`) — то есть изменения самой ветки.
// Основной сценарий — старт задачи: её ветка (ai/task/<id>) отребейчена на
// релизную ветку эпика, поэтому «свои» файлы задачи и код собранных задач
// эпика индексируются под веткой задачи, а всё остальное покрывает индекс main
// (rag/search.go). Полная индексация worktree здесь не нужна и дорога.
func (sess *Session) IndexRefDiffBackground(ctx context.Context, ref, base string) error {
	ref = strings.TrimSpace(ref)
	base = strings.TrimSpace(base)
	if ref == "" || base == "" {
		return fmt.Errorf("не заданы ветка и база для индексации")
	}
	dir := sess.projectDir()
	ex := sess.srv.gitExec
	job := apprag.RefDiffJob{Project: sess.project, Dir: dir, Base: base, Exec: ex, Loader: loadRefDiff, Sink: ragIndexSink{sess: sess}}
	return sess.startIndexBackground("изменения ветки "+ref, ref,
		func(bgctx context.Context, indexer apprag.Indexer) (*rag.IndexResult, string, bool, error) {
			return job.Run(bgctx, indexer, ref)
		})
}

// loadRefDiff — загрузчик изменений ветки относительно базы (инъекция для
// hermetic-тестов; порт apprag.DiffLoader).
var loadRefDiff apprag.DiffLoader = apprag.LoadRefDiff

// --- Хуки поддержания индекса и рабочей копии в актуальном состоянии ---

// refreshRagIndexBranch планирует фоновую переиндексацию ветки проекта.
// Ошибки (нет сессии, RAG недоступен, индексация уже идёт) — только в лог:
// git-flow и хуки доски не должны из-за индекса отказывать.
func (s *Server) refreshRagIndexBranch(project, ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	sess := s.session(project)
	if sess == nil {
		logging.For(project).Detailf("rag: переиндексация ветки %s пропущена — сессия не создана", ref)
		return
	}
	if err := sess.IndexRefBackground(context.Background(), ref); err != nil {
		logging.For(project).Detailf("rag: переиндексация ветки %s: %v", ref, err)
	}
}

// refreshRagIndexDir планирует фоновую индексацию каталога (рабочей копии
// проекта) под его веткой: после синхронизации с main точки ai/<имя> устарели и
// перекрывают свежие точки main по chunk_id (см. rag.Search.dropOverriddenByBranch).
func (s *Server) refreshRagIndexDir(project, dir, what string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	sess := s.session(project)
	if sess == nil {
		logging.For(project).Detailf("rag: индексация %s пропущена — сессия не создана", what)
		return
	}
	if err := sess.IndexDirBackground(context.Background(), dir, what); err != nil {
		logging.For(project).Detailf("rag: индексация %s: %v", what, err)
	}
}

// refreshRagTaskBranch планирует индексацию изменений ветки задачи относительно
// главной ветки. Вызывается при старте задачи (после создания worktree).
func (s *Server) refreshRagTaskBranch(project, taskBranch, main string) {
	if strings.TrimSpace(taskBranch) == "" {
		return
	}
	base := strings.TrimSpace(main)
	if base == "" {
		base = rag.MainBranch
	}
	sess := s.session(project)
	if sess == nil {
		logging.For(project).Detailf("rag: индексация ветки задачи %s пропущена — сессия не создана", taskBranch)
		return
	}
	if err := sess.IndexRefDiffBackground(context.Background(), taskBranch, base); err != nil {
		logging.For(project).Detailf("rag: индексация ветки задачи %s: %v", taskBranch, err)
	}
}

// syncAgentBranchWithMain — обёртка над apprag.WorkingCopySyncer (Этап 6, срез
// F3s): политика синхронизации рабочей копии с main переехала в
// application/rag. Сигнатура сохранена для существующих вызовов/тестов.
func (s *Server) syncAgentBranchWithMain(ctx context.Context, project string, inf workspace.Info, repo *git.Repo) bool {
	sy := apprag.WorkingCopySyncer{Exec: s.gitExec, Sink: syncSink{project: project}}
	return sy.Sync(ctx, apprag.SyncProject{Base: inf.GitBase, Branch: inf.GitBranch}, repo)
}

// syncSink — адаптер apprag.SyncSink поверх лога проекта.
type syncSink struct{ project string }

func (s syncSink) Detail(format string, args ...any) { logging.For(s.project).Detailf(format, args...) }
func (s syncSink) Warn(format string, args ...any)   { logging.For(s.project).Warnf(format, args...) }
func (s syncSink) Info(format string, args ...any)   { logging.For(s.project).Infof(format, args...) }

// syncAgentBranchAsync — фоновой вариант syncAgentBranchWithMain для хуков
// git-flow: не блокирует переход статуса/релиз. После успешного обновления
// копии индекс её ветки тоже пересобирается — иначе устаревшие точки ai/<имя>
// перекрывают свежие точки main по chunk_id (см. rag.Search.dropOverriddenByBranch).
func (s *Server) syncAgentBranchAsync(project string) {
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit || inf.Root == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		lock := s.mergeLock(project)
		lock.Lock()
		defer lock.Unlock()
		repo, err := s.repoOf(ctx, project)
		if err != nil {
			logging.For(project).Detailf("rag: синхрон рабочей копии с main: %v", err)
			return
		}
		if !s.syncAgentBranchWithMain(ctx, project, inf, repo) {
			return
		}
		s.refreshRagIndexDir(project, inf.Root, "рабочая копия проекта")
	}()
}

// afterMainChanged — единая точка реакции на изменение main: переиндексация
// ветки main (по ref) и фоновый синхрон рабочей копии агентов. Вызывается
// после успешного merge эпика в main (server/gitflow.go: releaseEpic).
func (s *Server) afterMainChanged(project, main string) {
	s.refreshRagIndexBranch(project, main)
	s.syncAgentBranchAsync(project)
}
