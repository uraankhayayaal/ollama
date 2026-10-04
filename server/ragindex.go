// Фоновая индексация RAG проекта (см. PLAN-2026-09-24-done-architect-intelligence.md, Ф-5, Р-2;
// версия по веткам — PLAN-2026-09-27-done-branch-aware-rag.md).
//
// Архитектор видит через RagIndexStatus, что проект не проиндексирован, и по
// промпту предлагает пользователю (AskUser) построить индекс в фоне. Мост
// IndexBackground (server/actions.go) вызывает Session.IndexBackground —
// индексация уходит в горутину и НЕ блокирует агентский цикл: архитектор
// продолжает проектирование через ReadFiles/ReadMap/LSP, а результат
// (файлы/чанки) отчитывается в лог проекта и chat.RoleStatus. Повторный
// запуск идемпотентен (IndexProject помечает прежние версии чанков устаревшими
// и грузит новые с тем же chunk_id) и защищён флагом сессии (повтор при уже
// идущей индексации отклоняется). Индексация ветко-осознанная: чанки получают
// branch/commit_sha, поэтому CodeSearch в ветке агента видит свою ветку и
// актуальный main, но не изменения соседних эпиков.

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai/chat"
	"ai/projects"
	"ai/rag"
)

// projectIndexer — узкий интерфейс векторной памяти для полной индексации
// проекта (реализует *rag.Client; выделен для hermetic-тестов без сети).
type projectIndexer interface {
	EnsureCollection(ctx context.Context) (int, error)
	IndexProject(ctx context.Context, projectName string, files []rag.IndexItem, opts rag.IndexOptions) (*rag.IndexResult, error)
	// BranchIndexed — есть ли активные точки ветки на этом коммите: хуки
	// поддержания индекса не должны переиндексировать ветку без изменений.
	BranchIndexed(ctx context.Context, projectName, branch, commit string) (bool, error)
}

// projectIndexerCloser — индексатор, который нужно закрыть после работы.
// *rag.Client реализует обе части (Close закрывает gRPC-соединение).
type projectIndexerCloser interface {
	projectIndexer
	Close() error
}

// buildProjectIndexer — фабрика клиента RAG для фоновой индексации. Замена
// (пакетная переменная) позволяет hermetic-тестам подставить фейковый
// индексатор без сети/Qdrant.
var buildProjectIndexer = func() (projectIndexerCloser, error) {
	c := rag.NewClientSafe(rag.Config{})
	if c == nil {
		return nil, errors.New("клиент RAG не создан (проверь QDRANT_ADDR и EMBEDDING_MODEL)")
	}
	return c, nil
}

// IndexBackground запускает фоновую индексацию RAG-памяти проекта (Ф-5, Р-2).
// Возвращается сразу (в отдельной горутине): агентский цикл не блокируется.
// Повторный вызов при уже идущей индексации — ошибка (один прогон на сессию).
// branch — необязательная ветка:
//
//   - пусто — индексируется рабочий каталог проекта под его текущей веткой
//     (rag.DetectIndexOptions);
//   - задана — индексируется СОДЕРЖИМОЕ ветки из git (по ref), а не рабочая
//     копия: checkout проекта стоит на ветке агента (ai/<имя>) и кода main в
//     нём нет, поэтому подписывать его как main нельзя (см. ragref.go).
func (sess *Session) IndexBackground(ctx context.Context, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch != "" {
		return sess.IndexRefBackground(ctx, branch)
	}
	dir := sess.projectDir()
	return sess.IndexDirBackground(ctx, dir, "проект")
}

// projectDir — каталог рабочей копии проекта из реестра (фолбэк projects.ProjectDir).
func (sess *Session) projectDir() string {
	if inf, err := sess.srv.reg.Get(sess.project); err == nil && inf.Root != "" {
		return inf.Root
	}
	return projects.ProjectDir(sess.project)
}

// indexRun — сама индексация, выполняемая в фоновой горутине: получает клиента
// RAG и возвращает сводку вместе с фактической веткой индексации. skipped=true —
// индекс ветки уже актуален, переиндексация не требовалась (прогон без записи
// точек): отчёт об этом отдельный, чтобы «актуальный индекс» не выглядел как
// «обновлено 0 файлов».
type indexRun func(ctx context.Context, indexer projectIndexerCloser) (res *rag.IndexResult, branch string, skipped bool, err error)

// startIndexBackground — общий запуск фоновой индексации: single-flight по
// сессии (повтор при идущей — ошибка), горутина в wg сессии, отчёт в лог и
// chat.RoleStatus. note — короткое описание источника для логов, branch —
// ветка индексации для сообщений пользователю.
func (sess *Session) startIndexBackground(note, branch string, run indexRun) error {
	cl, err := buildProjectIndexer()
	if err != nil {
		return err
	}

	key := indexSlotKey(note, branch)
	sess.mu.Lock()
	if sess.indexing[key] {
		sess.mu.Unlock()
		_ = cl.Close()
		return fmt.Errorf("фоновая индексация RAG уже запущена для проекта %s (%s)", sess.project, branchSuffix(branch))
	}
	if sess.indexing == nil {
		sess.indexing = map[string]bool{}
	}
	sess.indexing[key] = true
	// Горутина в wg сессии: гарантирует, что Stop/wait сессии дождутся
	// индексации, не оставляя «висячих» соединений после завершения запуска.
	sess.wg.Add(1)
	sess.mu.Unlock()

	go func() {
		defer sess.wg.Done()
		defer func() {
			sess.mu.Lock()
			delete(sess.indexing, key)
			sess.mu.Unlock()
		}()
		defer cl.Close()
		// Фоновая индексация НЕ привязывается к контексту вызывающего
		// (REST-запрос/раунд агента завершается сразу после запуска): и то и
		// другое к первому embed-вызову уже отменено, и прогон падает с
		// «context canceled» на определение размерности. Бужется контекст
		// жизненного цикла процесса: индексация сама себя завершает.
		bgctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sess.runBackgroundIndex(bgctx, cl, note, run)
	}()

	sess.append(chat.RoleStatus,
		fmt.Sprintf("Запущена фоновая индексация RAG-индекса проекта %s%s.", sess.project, branchSuffix(branch)), "", "", nil)
	return nil
}

// indexSlotKey — слот single-flight: индексации разных веток не блокируют друг
// друга, одной и той же — блокируют. Для каталогов вне git (ветка не
// определилась) слотом служит путь источника: переиндексация проекта и worktree
// задачи тогда не отклоняют друг друга.
func indexSlotKey(note, branch string) string {
	if b := strings.TrimSpace(branch); b != "" {
		return "branch:" + b
	}
	return "src:" + strings.TrimSpace(note)
}

// branchSuffix — « (ветка X)» для сообщений о фоновой индексации.
func branchSuffix(branch string) string {
	if b := strings.TrimSpace(branch); b != "" {
		return " (ветка " + b + ")"
	}
	return ""
}

// IndexDirBackground индексирует произвольный каталог проекта (temp/<имя> или
// worktree задачи) под веткой, определённой по его состоянию git. what —
// подпись источника для логов («проект», «worktree задачи T-01»).
func (sess *Session) IndexDirBackground(ctx context.Context, dir, what string) error {
	what = strings.TrimSpace(what)
	if what == "" {
		what = "проект"
	}
	if strings.TrimSpace(dir) == "" {
		return errors.New("не определён каталог проекта для индексации")
	}
	// Ветка/коммит определяются до запуска: они попадают в сообщение о старте,
	// а каталог мог исчезнуть уже после (типовая гонка с очисткой temp/).
	opts := rag.DetectIndexOptions(dir)
	return sess.startIndexBackground(what, opts.Branch,
		func(bgctx context.Context, indexer projectIndexerCloser) (*rag.IndexResult, string, bool, error) {
			sess.log.Infof("rag: фоновая индексация %s (%s): обход %s (ветка %s, коммит %s)",
				sess.project, what, dir, opts.Branch, orEmpty(opts.CommitSHA))
			res, err := indexProjectRAG(bgctx, indexer, sess.project, dir, opts)
			return res, opts.Branch, false, err
		})
}

// runBackgroundIndex выполняет фоновую индексацию и отчитывается в лог и чат
// (chat.RoleStatus). Любая ошибка — только отчёт: генерация/цикл не деградируют.
func (sess *Session) runBackgroundIndex(ctx context.Context, indexer projectIndexerCloser, note string, run indexRun) {
	res, branch, skipped, err := run(ctx, indexer)
	if err != nil {
		sess.log.Warnf("rag: фоновая индексация %s (%s): %v", sess.project, note, err)
		sess.append(chat.RoleStatus, "Фоновая индексация RAG не удалась: "+err.Error(), "", "", nil)
		return
	}
	if res == nil {
		res = &rag.IndexResult{}
	}
	if skipped {
		sess.log.Infof("rag: фоновая индексация %s (%s): индекс ветки %s актуален", sess.project, note, branch)
		sess.append(chat.RoleStatus,
			fmt.Sprintf("RAG-индекс проекта актуален, повторная индексация не требовалась (ветка %s)", branch),
			"", "", nil)
		return
	}
	sess.log.Infof("rag: фоновая индексация %s завершена: файлов %d, чанков %d",
		sess.project, res.Files, res.Chunks)
	for _, e := range res.Errors {
		sess.log.Warnf("rag: фоновая индексация %s: %s", sess.project, e)
	}
	sess.append(chat.RoleStatus,
		fmt.Sprintf("RAG-индекс проекта обновлён: файлов %d, чанков %d, ошибок %d (ветка %s)",
			res.Files, res.Chunks, len(res.Errors), branch), "", "", nil)
}

// orEmpty — замена пустого значения (для логов).
func orEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// indexProjectRAG индексирует проект целиком: сначала коллекция (create-if-
// not-exists), затем сбор файлов и IndexProject с веткой/коммитом (полная
// переиндексация ветки — прежние версии чанков помечаются устаревшими,
// прогон идемпотентен).
func indexProjectRAG(ctx context.Context, indexer projectIndexer, project, dir string, opts rag.IndexOptions) (*rag.IndexResult, error) {
	if _, err := indexer.EnsureCollection(ctx); err != nil {
		return nil, fmt.Errorf("подготовка коллекции: %w", err)
	}
	items, err := collectIndexItems(ctx, dir, rag.WalkProject)
	if err != nil {
		return nil, fmt.Errorf("обход %s: %w", dir, err)
	}
	return indexer.IndexProject(ctx, project, items, opts)
}

// walkerFn — функция обхода проекта (обычно rag.WalkProject; инъекция для
// hermetic-тестов, чтобы не создавать реальные файлы).
type walkerFn func(dir string) ([]string, error)

// collectIndexItems обходит проект (walker) и собирает файлы в IndexItem'ы:
// путь, область (ScopeForPath) и содержимое. Нечитаемые файлы пропускаются,
// прерывание контекста распространяется наружу.
func collectIndexItems(ctx context.Context, dir string, walk walkerFn) ([]rag.IndexItem, error) {
	files, err := walk(dir)
	if err != nil {
		return nil, err
	}
	items := make([]rag.IndexItem, 0, len(files))
	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if rerr != nil {
			continue
		}
		items = append(items, rag.IndexItem{
			Path:    rel,
			Scope:   rag.ScopeForPath(rel),
			Content: string(content),
		})
	}
	return items, nil
}
