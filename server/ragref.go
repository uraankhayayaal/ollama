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
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"ai/forges"
	"ai/gitops"
	"ai/logging"
	"ai/rag"
	"ai/workspace"
	"time"
)

// maxRefFileBytes — предел размера файла, который читается из ветки (аналог
// rag.maxIndexFileSize: крупные вендоренные блоки в индекс не идут).
const maxRefFileBytes = 2_000_000

// refSnapshot — содержимое ветки/ref, готовое к индексации.
type refSnapshot struct {
	// Branch — имя ветки, под которым точки попадут в индекс (payload branch).
	Branch string
	// CommitSHA — коммит, снятый снапшот (версия индексации); пусто — без git.
	CommitSHA string
	// Items — файлы ветки для полной индексации.
	Items []rag.IndexItem
}

// refItemsLoader — снапшот ветки по ref. Аргументы: git-исполнитель сервера,
// каталог репозитория и имя ветки. Заменяется в hermetic-тестах.
type refItemsLoader func(ctx context.Context, ex gitops.Executor, dir, ref string) (refSnapshot, error)

// loadRefItems — текущий загрузчик снапшота (пакетная переменная для тестов).
var loadRefItems refItemsLoader = collectRefItems

// branchIndexUpToDate — активные точки ветки на этом коммите уже есть:
// переиндексация не нужна (проверка дешевле прогона и индекс не трогает).
func branchIndexUpToDate(ctx context.Context, indexer projectIndexer, project, branch, commit string) (bool, error) {
	if strings.TrimSpace(commit) == "" {
		return false, nil
	}
	done, err := indexer.BranchIndexed(ctx, project, branch, commit)
	if err != nil {
		return false, err
	}
	if done {
		logging.For(project).Infof("rag: индекс ветки %s (%s) актуален — повторная индексация не нужна",
			branch, commit[:min(len(commit), 8)])
	}
	return done, nil
}

// collectRefItems читает файлы ветки прямо из git: `git ls-tree -r -z --long`
// даёт список blob-ов с размерами, содержимое — `git show ref:<путь>`. Рабочая
// копия не читается и не меняется, поэтому индекс ветки всегда соответствует
// её содержимому, даже если checkout проекта отстал от main.
func collectRefItems(ctx context.Context, ex gitops.Executor, dir, ref string) (refSnapshot, error) {
	if ex == nil {
		return refSnapshot{}, fmt.Errorf("не задан git-исполнитель")
	}
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(ref) == "" {
		return refSnapshot{}, fmt.Errorf("не заданы каталог и ветка для индексации")
	}
	snap := refSnapshot{Branch: ref}

	out, err := ex.Exec(ctx, dir, "git", "ls-tree", "-r", "-z", "--long", ref)
	if err != nil {
		return refSnapshot{}, fmt.Errorf("git ls-tree %s: %w", ref, err)
	}
	for _, rec := range strings.Split(out, "\x00") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		path, size, ok := parseLsTreeEntry(rec)
		if !ok || !indexablePath(path) {
			continue
		}
		// Размер известен из ls-tree: пустые и крупные файлы не читаем вовсе.
		if size <= 0 || size > maxRefFileBytes {
			continue
		}
		content, cerr := ex.Exec(ctx, dir, "git", "show", ref+":"+path)
		if cerr != nil {
			logging.Warnf("rag: git show %s:%s: %v", ref, path, cerr)
			continue
		}
		if !indexableContent(content) {
			continue
		}
		snap.Items = append(snap.Items, rag.IndexItem{
			Path:    path,
			Scope:   rag.ScopeForPath(path),
			Content: content,
		})
	}

	// Коммит ветки — версия индексации. Без него точки всё равно заменяют друг
	// друга (проект без git), но с ним повторный прогон идемпотентен.
	if sha, serr := ex.Exec(ctx, dir, "git", "rev-parse", ref); serr == nil {
		snap.CommitSHA = strings.TrimSpace(sha)
	}
	return snap, nil
}

// parseLsTreeEntry разбирает запись `git ls-tree --long`: метаданные до табуляции
// (mode type sha size) и путь после. Возвращает путь, размер и признак, что
// запись разобрана и это обычный blob (не симлинк и не поддерево).
func parseLsTreeEntry(rec string) (path string, size int64, ok bool) {
	meta, path, found := strings.Cut(rec, "\t")
	if !found {
		return "", 0, false
	}
	fields := strings.Fields(meta)
	if len(fields) < 4 || fields[1] != "blob" || fields[0] == "120000" {
		return "", 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64)
	if err != nil {
		return "", 0, false
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", 0, false
	}
	return path, n, true
}

// indexablePath повторяет политику отбора rag.WalkProject для пути из ветки:
// служебные каталоги (.git, node_modules, build, ...) и скрытые файлы/каталоги
// (.env, .github) не индексируются.
func indexablePath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || strings.HasPrefix(path, "/") {
		return false
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			return false
		}
		if strings.HasPrefix(seg, ".") || forges.IsIgnoredDir(seg) {
			return false
		}
	}
	return true
}

// indexableContent отбрасывает пустые, крупные и бинарные (NUL в префиксе)
// файлы — те же правила, что у обхода рабочей копии.
func indexableContent(content string) bool {
	if content == "" || len(content) > maxRefFileBytes {
		return false
	}
	probe := content
	if len(probe) > 1024 {
		probe = probe[:1024]
	}
	return !bytes.ContainsRune([]byte(probe), 0)
}

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
		func(bgctx context.Context, indexer projectIndexerCloser) (*rag.IndexResult, string, bool, error) {
			snap, err := loadRefItems(bgctx, ex, dir, ref)
			if err != nil {
				return nil, ref, false, err
			}
			skip, err := branchIndexUpToDate(bgctx, indexer, sess.project, snap.Branch, snap.CommitSHA)
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
		func(bgctx context.Context, indexer projectIndexerCloser) (*rag.IndexResult, string, bool, error) {
			snap, err := loadRefDiff(bgctx, ex, dir, ref, base)
			if err != nil {
				return nil, ref, false, err
			}
			skip, err := branchIndexUpToDate(bgctx, indexer, sess.project, snap.Branch, snap.CommitSHA)
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
var loadRefDiff refDiffLoader = collectRefDiff

// refDiffLoader — файлы, отличающиеся ref от base.
type refDiffLoader func(ctx context.Context, ex gitops.Executor, dir, ref, base string) (refSnapshot, error)

// collectRefDiff собирает файлы `git diff --name-only base...ref` и читает их
// содержимое из ref: содержимое рабочей копии не используется (она может
// отставать), коммит берётся по ref.
func collectRefDiff(ctx context.Context, ex gitops.Executor, dir, ref, base string) (refSnapshot, error) {
	if ex == nil {
		return refSnapshot{}, fmt.Errorf("не задан git-исполнитель")
	}
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(ref) == "" || strings.TrimSpace(base) == "" {
		return refSnapshot{}, fmt.Errorf("не заданы каталог и ветки для индексации изменений")
	}
	out, err := ex.Exec(ctx, dir, "git", "diff", "--name-only", "-z", base+"..."+ref)
	if err != nil {
		return refSnapshot{}, fmt.Errorf("git diff %s...%s: %w", base, ref, err)
	}
	files := make([]string, 0, 8)
	for _, p := range strings.Split(out, "\x00") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		size, ok := refFileSize(ctx, ex, dir, ref, p)
		if !ok || size > maxRefFileBytes {
			continue
		}
		files = append(files, p)
	}
	snap := refSnapshot{Branch: ref, Items: []rag.IndexItem{}}
	if sha, serr := ex.Exec(ctx, dir, "git", "rev-parse", ref); serr == nil {
		snap.CommitSHA = strings.TrimSpace(sha)
	}
	for _, p := range files {
		content, cerr := ex.Exec(ctx, dir, "git", "show", ref+":"+p)
		if cerr != nil {
			logging.Warnf("rag: git show %s:%s: %v", ref, p, cerr)
			continue
		}
		if !indexableContent(content) {
			continue
		}
		snap.Items = append(snap.Items, rag.IndexItem{
			Path:    p,
			Scope:   rag.ScopeForPath(p),
			Content: content,
		})
	}
	return snap, nil
}

// refFileSize — размер blob-а файла в ветке (`git cat-file -s ref:path`):
// нужен, чтобы не читать крупные файлы и не заводить одинаковые точки для
// удалённых/переименованных путей. ok=false — пути в ветке нет (удалён) либо
// путь не индексируется.
func refFileSize(ctx context.Context, ex gitops.Executor, dir, ref, path string) (int64, bool) {
	if !indexablePath(path) {
		return 0, false
	}
	out, err := ex.Exec(ctx, dir, "git", "cat-file", "-s", ref+":"+path)
	if err != nil {
		return 0, false
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if perr != nil {
		return 0, false
	}
	return n, true
}

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
func (s *Server) syncAgentBranchWithMain(ctx context.Context, project string, inf workspace.Info, repo *gitops.Repo) bool {
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
