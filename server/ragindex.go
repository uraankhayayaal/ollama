// Фоновая индексация RAG проекта (см. PLAN-2026-09-24-done-architect-intelligence.md, Ф-5, Р-2).
//
// Архитектор видит через RagIndexStatus, что проект не проиндексирован, и по
// промпту предлагает пользователю (AskUser) построить индекс в фоне. Мост
// IndexBackground (server/actions.go) вызывает Session.IndexBackground —
// индексация уходит в горутину и НЕ блокирует агентский цикл: архитектор
// продолжает проектирование через ReadFiles/ReadMap/LSP, а результат
// (файлы/чанки) отчитывается в лог проекта и chat.RoleStatus. Повторный
// запуск идемпотентен (IndexProject сперва удаляет точки проекта) и защищён
// флагом сессии (повтор при уже идущей индексации отклоняется).

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"ai/chat"
	"ai/projects"
	"ai/rag"
)

// projectIndexer — узкий интерфейс векторной памяти для полной индексации
// проекта (реализует *rag.Client; выделен для hermetic-тестов без сети).
type projectIndexer interface {
	EnsureCollection(ctx context.Context) (int, error)
	IndexProject(ctx context.Context, projectName string, files []rag.IndexItem) (*rag.IndexResult, error)
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
func (sess *Session) IndexBackground(ctx context.Context) error {
	cl, err := buildProjectIndexer()
	if err != nil {
		return err
	}

	sess.mu.Lock()
	if sess.indexing {
		sess.mu.Unlock()
		_ = cl.Close()
		return fmt.Errorf("фоновая индексация RAG уже запущена для проекта %s", sess.project)
	}
	sess.indexing = true
	// Горутина в wg сессии: гарантирует, что Stop/wait сессии дождутся
	// индексации, не оставляя «висячих» соединений после завершения запуска.
	sess.wg.Add(1)
	sess.mu.Unlock()

	go func() {
		defer sess.wg.Done()
		defer func() {
			sess.mu.Lock()
			sess.indexing = false
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
		sess.runBackgroundIndex(bgctx, cl)
	}()

	sess.append(chat.RoleStatus,
		fmt.Sprintf("Запущена фоновая индексация RAG-индекса проекта %s.", sess.project), "", "", nil)
	return nil
}

// runBackgroundIndex выполняет полную индексацию проекта в векторную память и
// отчитывается в лог и чат (RoleStatus). Любая ошибка — только отчёт:
// генерация/цикл не деградируют.
func (sess *Session) runBackgroundIndex(ctx context.Context, indexer projectIndexer) {
	dir := projects.ProjectDir(sess.project)
	if inf, err := sess.srv.reg.Get(sess.project); err == nil {
		dir = inf.Root
	}

	sess.log.Infof("rag: фоновая индексация %s: начинаю обход %s", sess.project, dir)
	res, err := indexProjectRAG(ctx, indexer, sess.project, dir)
	if err != nil {
		sess.log.Warnf("rag: фоновая индексация %s: %v", sess.project, err)
		sess.append(chat.RoleStatus, "Фоновая индексация RAG не удалась: "+err.Error(), "", "", nil)
		return
	}
	sess.log.Infof("rag: фоновая индексация %s завершена: файлов %d, чанков %d",
		sess.project, res.Files, res.Chunks)
	for _, e := range res.Errors {
		sess.log.Warnf("rag: фоновая индексация %s: %s", sess.project, e)
	}
	sess.append(chat.RoleStatus,
		fmt.Sprintf("RAG-индекс проекта обновлён: файлов %d, чанков %d, ошибок %d",
			res.Files, res.Chunks, len(res.Errors)), "", "", nil)
}

// indexProjectRAG индексирует проект целиком: сначала коллекция (create-if-
// not-exists), затем сбор файлов и IndexProject (полная переиндексация —
// старые точки проекта удаляются до загрузки, прогон идемпотентен).
func indexProjectRAG(ctx context.Context, indexer projectIndexer, project, dir string) (*rag.IndexResult, error) {
	if _, err := indexer.EnsureCollection(ctx); err != nil {
		return nil, fmt.Errorf("подготовка коллекции: %w", err)
	}
	items, err := collectIndexItems(ctx, dir, rag.WalkProject)
	if err != nil {
		return nil, fmt.Errorf("обход %s: %w", dir, err)
	}
	return indexer.IndexProject(ctx, project, items)
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
