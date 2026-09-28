package runner

// Ф-5: частичная переиндексация RAG после мутаций кода.
//
// Агент-разработчик (через встроенный *tools.FileOps + поле RAG) реализует
// интерфейс Reindexer. После раунда, в котором были мутации файлов, раннер
// (в том же хуке, что и LSP-авто-лечение, runner/autofix.go) переиндексирует
// затронутые файлы в векторную память Qdrant: прежние версии чанков помечаются
// устаревшими (replaced_by = коммит), новые — загружаются с chunk_id/
// commit_sha (см. PLAN-2026-09-27-done-branch-aware-rag.md, Р-3/Р-7). Повтор с тем
// же коммитом идемпотентен, удалённые мутацией файлы помечаются устаревшими.
//
// Очередь затронутых файлов жёстко одна (FileOps.touched): и LSP-хук, и
// reindex получают её из ОДНОГО дрена (TakeTouched) в runner.go, чтобы не
// конкурировать за неё. Переиндексация, в отличие от LSP, не подмешивает
// промпты модели — просто обновляет векторную память для последующих
// CodeSearch/контекста плана.
//
// Поведение управляется RAG_AUTO_REINDEX (по умолчанию 0 — выключено:
// индекс собирается явной командой `go run . index <проект>`). При
// недоступном Qdrant переиндексация деградирует тихо (возвращает ошибку,
// которую раннер логирует; генерация не падает), как и остальные
// RAG-инструменты.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai/rag"
)

// reindexOnEnv — env-флаг авто-обновления RAG-индекса после мутаций (Ф-5).
const reindexOnEnv = "RAG_AUTO_REINDEX"

// Reindexer — необязательный интерфейс агента для частичной переиндексации
// RAG (Ф-5). Реализуется агентами-разработчиками: переданный список файлов
// (из единой очереди touched, дренированной раннером) перечитывается из
// OutputDir и заново грузится в векторную память.
type Reindexer interface {
	// ReindexTouched переиндексирует файлы, затронутые мутацией за раунд.
	// Возвращает число успешно переиндексированных файлов; ошибка (например,
	// недоступный Qdrant) — degrade: логируется, шаг не падает.
	ReindexTouched(touched []string) (reindexed int, err error)
}

// ReindexClient — узкий интерфейс векторной памяти, необходимый для
// переиндексации (Ф-5). Реализуется *rag.Client.
type ReindexClient interface {
	// IndexFile заменяет векторы файла в ветке: прежние версии чанков
	// помечаются устаревшими, новые грузятся с chunk_id/commit_sha.
	// Идемпотентна — повторный прогон с тем же коммитом не плодит дубли.
	IndexFile(ctx context.Context, projectName, relPath, scope, content string, opts rag.IndexOptions) (int, error)
	// DeleteFile помечает устаревшими все чанки файла в ветке.
	DeleteFile(ctx context.Context, projectName, relPath string, opts rag.IndexOptions) error
}

// ReindexFiles переиндексирует список относительных путей проекта: для каждого
// файла читается текущее содержимое из contentDir и заново грузится в индекс
// ветки opts (scope — через scopeOf, обычно rag.ScopeForPath); отсутствующие на
// диске файлы (удалены мутацией) помечаются устаревшими. Версия индексации
// (branch/commit_sha) определяется вызывающей стороной через
// rag.DetectIndexOptions(contentDir) — так поиск CodeSearch в ветке агента
// видит только её код и актуальный main (Р-7). Возвращает число успешно
// обработанных файлов; первая же ошибка останавливает переиндексацию —
// вызывающий логирует и продолжает генерацию (degrade).
func ReindexFiles(ctx context.Context, cl ReindexClient, scopeOf func(rel string) string, contentDir, project string, files []string, opts rag.IndexOptions) (int, error) {
	if cl == nil || len(files) == 0 {
		return 0, nil
	}
	reindexed := 0
	for _, rel := range files {
		rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
		if rel == "" || rel == "." {
			continue
		}
		content, err := os.ReadFile(filepath.Join(contentDir, filepath.FromSlash(rel)))
		if err != nil {
			if os.IsNotExist(err) {
				// Файл удалён мутацией — его версии в ветке устаревают, висящих
				// «актуальных» векторов в памяти не остаётся.
				if derr := cl.DeleteFile(ctx, project, rel, opts); derr != nil {
					return reindexed, fmt.Errorf("delete %s из индекса: %w", rel, derr)
				}
				reindexed++
				continue
			}
			return reindexed, fmt.Errorf("read %s: %w", rel, err)
		}
		scope := ""
		if scopeOf != nil {
			scope = scopeOf(rel)
		}
		if _, ierr := cl.IndexFile(ctx, project, rel, scope, string(content), opts); ierr != nil {
			return reindexed, fmt.Errorf("index %s: %w", rel, ierr)
		}
		reindexed++
	}
	return reindexed, nil
}

// reindexEnabled сообщает, включена ли частичная переиндексация RAG
// (RAG_AUTO_REINDEX). По умолчанию выключено (0): индекс собирается явной
// командой `go run . index`. Признак распознаётся как для LSP_AUTO_FIX:
// 1/true/on/yes — включено, прочее/пусто — выключено.
func reindexEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(reindexOnEnv))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}
