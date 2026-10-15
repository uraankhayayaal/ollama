// Фоновая индексация RAG: single-flight по слоту (ветке/источнику), запуск в
// горутине и отчёт о ходе (срез F3m Этапа 6).
//
// Логика вынесена из server/ragindex.go: сервер оставляет построение конкретного
// прогона (каталог/ветка/дифф), а общая оркестрация — запуск фонового прогона,
// отказ повторного запуска для той же ветки, отчёт в лог и чат — живёт здесь.
// Отчёт адресуется портом IndexSink; проект и фабрика индексатора передаются при
// создании.
package rag

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"ai/domain"
)

// IndexSink — приёмник отчётов фоновой индексации: информационные/тревожные
// строки в лог проекта и статусные сообщения в чат.
type IndexSink interface {
	Info(msg string)
	Warn(msg string)
	Status(msg string)
}

// IndexerFactory создаёт индексатор на один фоновый прогон; closer вызывается
// после завершения (освобождение соединения). Ошибка — RAG недоступен, прогон
// не стартует.
type IndexerFactory func() (Indexer, io.Closer, error)

// IndexRun — конкретный прогон индексации: возвращает результат, фактическую
// ветку и признак «индекс ветки уже актуален» (skipped — отдельный отчёт, чтобы
// «актуальный индекс» не выглядел как «обновлено 0 файлов»).
type IndexRun func(ctx context.Context, indexer Indexer) (*domain.IndexResult, string, bool, error)

// BackgroundIndexer — single-flight фоновых индексаций RAG одного проекта.
// Слот — ветка (или путь источника для каталогов вне git): индексации разных
// веток идут параллельно, повторный запуск той же ветки отклоняется. Один
// экземпляр на сессию проекта.
type BackgroundIndexer struct {
	project    string
	sink       IndexSink
	newIndexer IndexerFactory

	mu       sync.Mutex
	indexing map[string]bool
	wg       sync.WaitGroup
}

// NewBackgroundIndexer создаёт оркестратор фоновых индексаций проекта.
func NewBackgroundIndexer(project string, sink IndexSink, newIndexer IndexerFactory) *BackgroundIndexer {
	return &BackgroundIndexer{
		project:    project,
		sink:       sink,
		newIndexer: newIndexer,
		indexing:   map[string]bool{},
	}
}

// Start запускает фоновый прогон. Возвращается сразу (индексация идёт в
// отдельной горутине и НЕ привязывается к контексту вызывающего — REST-запрос/
// раунд агента к первому embed-вызову уже отменены). Повторный запуск для того
// же слота (ветки) — ошибка.
func (b *BackgroundIndexer) Start(note, branch string, run IndexRun) error {
	indexer, closer, err := b.newIndexer()
	if err != nil {
		return err
	}

	key := indexSlotKey(note, branch)
	b.mu.Lock()
	if b.indexing[key] {
		b.mu.Unlock()
		if closer != nil {
			_ = closer.Close()
		}
		return fmt.Errorf("фоновая индексация RAG уже запущена для проекта %s (%s)", b.project, branchSuffix(branch))
	}
	b.indexing[key] = true
	b.wg.Add(1)
	b.mu.Unlock()

	go func() {
		defer b.wg.Done()
		defer func() {
			b.mu.Lock()
			delete(b.indexing, key)
			b.mu.Unlock()
		}()
		if closer != nil {
			defer closer.Close()
		}
		bgctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		b.report(bgctx, indexer, note, run)
	}()

	b.sink.Status(fmt.Sprintf("Запущена фоновая индексация RAG-индекса проекта %s%s.", b.project, branchSuffix(branch)))
	return nil
}

// Wait ждёт завершения всех фоновых прогонов.
func (b *BackgroundIndexer) Wait() { b.wg.Wait() }

// report выполняет прогон и отчитывается в лог и чат (IndexSink). Любая
// ошибка — только отчёт: генерация/цикл не деградируют.
func (b *BackgroundIndexer) report(ctx context.Context, indexer Indexer, note string, run IndexRun) {
	res, branch, skipped, err := run(ctx, indexer)
	if err != nil {
		b.sink.Warn(fmt.Sprintf("rag: фоновая индексация %s (%s): %v", b.project, note, err))
		b.sink.Status("Фоновая индексация RAG не удалась: " + err.Error())
		return
	}
	if res == nil {
		res = &domain.IndexResult{}
	}
	if skipped {
		b.sink.Info(fmt.Sprintf("rag: фоновая индексация %s (%s): индекс ветки %s актуален", b.project, note, branch))
		b.sink.Status(fmt.Sprintf("RAG-индекс проекта актуален, повторная индексация не требовалась (ветка %s)", branch))
		return
	}
	b.sink.Info(fmt.Sprintf("rag: фоновая индексация %s завершена: файлов %d, чанков %d", b.project, res.Files, res.Chunks))
	for _, e := range res.Errors {
		b.sink.Warn(fmt.Sprintf("rag: фоновая индексация %s: %s", b.project, e))
	}
	b.sink.Status(fmt.Sprintf("RAG-индекс проекта обновлён: файлов %d, чанков %d, ошибок %d (ветка %s)",
		res.Files, res.Chunks, len(res.Errors), branch))
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
