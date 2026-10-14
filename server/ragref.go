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

// refItemsLoader — снапшот ветки по ref. Аргументы: git-исполнитель сервера,
// каталог репозитория и имя ветки. Заменяется в hermetic-тестах.
type refItemsLoader func(ctx context.Context, ex git.Executor, dir, ref string) (apprag.RefSnapshot, error)

// loadRefItems — текущий загрузчик снапшота (пакетная переменная для тестов).
var loadRefItems refItemsLoader = apprag.LoadRefItems

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
	return sess.startIndexBackground("ветка "+ref, ref,
		func(bgctx context.Context, indexer apprag.Indexer) (*rag.IndexResult, string, bool, error) {
			snap, err := loadRefItems(bgctx, ex, dir, ref)
			if err != nil {
				return nil, ref, false, err
			}
			skip, err := apprag.BranchIndexUpToDate(bgctx, indexer, sess.project, snap.Branch, snap.CommitSHA)
			if err != nil || skip {
				return &rag.IndexResult{}, snap.Branch, skip, err
			}
			sess.log.Infof("rag: фоновая индексация %s: ветка %s из git (%s), файлов %d, коммит %s",
				sess.project, ref, dir, len(snap.Items), orEmpty(snap.CommitSHA))
			if _, err := indexer.EnsureCollection(bgctx); err != nil {
				return nil, ref, false, fmt.Errorf("подготовка коллекции: %w", err)
			}
			res, err := indexer.IndexProject(bgctx, sess.project, snap.Items, rag.IndexOptions{
				Branch:    snap.Branch,
				CommitSHA: snap.CommitSHA,
			})
			return res, snap.Branch, false, err
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
	return sess.startIndexBackground("изменения ветки "+ref, ref,
		func(bgctx context.Context, indexer apprag.Indexer) (*rag.IndexResult, string, bool, error) {
			snap, err := loadRefDiff(bgctx, ex, dir, ref, base)
			if err != nil {
				return nil, ref, false, err
			}
			skip, err := apprag.BranchIndexUpToDate(bgctx, indexer, sess.project, snap.Branch, snap.CommitSHA)
			if err != nil || skip {
				return &rag.IndexResult{}, snap.Branch, skip, err
			}
			sess.log.Infof("rag: фоновая индексация %s: изменения ветки %s относительно %s (%s), файлов %d, коммит %s",
				sess.project, ref, base, dir, len(snap.Items), orEmpty(snap.CommitSHA))
			if _, err := indexer.EnsureCollection(bgctx); err != nil {
				return nil, ref, false, fmt.Errorf("подготовка коллекции: %w", err)
			}
			res, err := indexer.IndexProject(bgctx, sess.project, snap.Items, rag.IndexOptions{
				Branch:    snap.Branch,
				CommitSHA: snap.CommitSHA,
			})
			return res, snap.Branch, false, err
		})
}

// loadRefDiff — загрузчик изменений ветки (инъекция для hermetic-тестов).
var loadRefDiff refDiffLoader = apprag.LoadRefDiff

// refDiffLoader — файлы, отличающиеся ref от base.
type refDiffLoader func(ctx context.Context, ex git.Executor, dir, ref, base string) (apprag.RefSnapshot, error)

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

// syncAgentBranchWithMain подтягивает рабочую копию агентов (temp/<имя> на ветке
// ai/<имя>) к актуальному main: `git merge --no-ff` main в ветку проекта.
// Пропуск (только лог) при грязном дереве, отсутствии main или конфликте —
// незакоммиченные правки агентов теряться не должны: перед merge проверяется
// чистота, а конфликтный merge откатывается (git merge --abort), иначе рабочая
// копия осталась бы с конфликтными маркерами. Возвращает true, только если
// рабочая копия реально обновилась (HEAD сдвинулся) — иначе переиндексация её
// ветки была бы напрасной: она стоит эмбеддингов всего проекта.
func (s *Server) syncAgentBranchWithMain(ctx context.Context, project string, inf workspace.Info, repo *git.Repo) bool {
	main := strings.TrimSpace(inf.GitBase)
	agentBranch := strings.TrimSpace(inf.GitBranch)
	if repo == nil || main == "" || agentBranch == "" || main == agentBranch {
		return false
	}
	if repo.Branch != "" && repo.Branch != agentBranch {
		// checkout занят другой веткой (например, служебный checkout перед
		// созданием worktree) — переключение делает вызывающий код.
		logging.For(project).Detailf("rag: синхрон рабочей копии с main пропущен — checkout на ветке %s", repo.Branch)
		return false
	}
	dirty, err := repo.Dirty(ctx)
	if err != nil {
		logging.For(project).Detailf("rag: синхрон рабочей копии с main: %v", err)
		return false
	}
	if dirty {
		logging.For(project).Detailf("rag: синхрон рабочей копии с main пропущен — есть незакоммиченные правки")
		return false
	}
	headBefore := s.headSHA(ctx, repo.Root)
	if _, err := repo.MergeBranch(ctx, main, fmt.Sprintf("%s: синхрон рабочей копии с %s", agentBranch, main)); err != nil {
		// Конфликт: возвращаем дерево к состоянию до merge, иначе следующий
		// агент увидит конфликтные маркеры, а индекс — мусор.
		s.abortMerge(ctx, repo.Root)
		logging.For(project).Warnf("rag: синхрон рабочей копии с %s: %v", main, err)
		return false
	}
	if head := s.headSHA(ctx, repo.Root); headBefore != "" && head == headBefore {
		logging.For(project).Detailf("rag: рабочая копия %s уже актуальна относительно %s", agentBranch, main)
		return false
	}
	logging.For(project).Infof("gitflow: рабочая копия %s синхронизирована с %s", agentBranch, main)
	return true
}

// headSHA — текущий коммит рабочей копии (пустая строка, если не git или нет
// коммитов).
func (s *Server) headSHA(ctx context.Context, dir string) string {
	if s.gitExec == nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	out, err := s.gitExec.Exec(ctx, dir, "git", "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// abortMerge откатывает неудачный merge в рабочей копии. Ошибка игнорируется:
// состояние merge и так лучше, чем паника, но молча не оставляем.
func (s *Server) abortMerge(ctx context.Context, dir string) {
	if s.gitExec == nil || strings.TrimSpace(dir) == "" {
		return
	}
	if _, err := s.gitExec.Exec(ctx, dir, "git", "merge", "--abort"); err != nil {
		logging.For(dir).Detailf("rag: git merge --abort: %v", err)
	}
}

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
