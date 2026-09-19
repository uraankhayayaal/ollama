// Индексация чанков в Qdrant (см. PLAN-qdrant.md, Ф-2).
//
// Чанк становится точкой коллекции с payload (project_name/file_path/
// start_line/end_line/code_content/scope). ID точки детерминированный
// (хэш проекта+файла+начала чанка), поэтому повторный upsert не плодит
// дубликаты, а при переиндексации файла/проекта старые точки сначала
// удаляются фильтром по payload.

package rag

import (
	"context"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/qdrant/go-client/qdrant"
)

// Ключи payload чанка в Qdrant.
const (
	PayloadProject   = "project_name"
	PayloadFile      = "file_path"
	PayloadStartLine = "start_line"
	PayloadEndLine   = "end_line"
	PayloadCode      = "code_content"
	PayloadScope     = "scope"
)

// indexBatchSize — максимум точек в одном Upsert-вызове.
const indexBatchSize = 64

// IndexItem — один файл для полной индексации проекта.
type IndexItem struct {
	Path    string // относительный slash-путь от корня проекта
	Scope   string // область проекта (каталог первого уровня, "root" для корня)
	Content string
}

// IndexResult — сводка индексации проекта.
type IndexResult struct {
	Files  int      // сколько файлов исходно взято в обработку
	Chunks int      // сколько чанков успешно загружено
	Errors []string // источники, не загруженные по ошибке
}

// IndexProject заново индексирует список файлов проекта: сначала удаляет
// ВСЕ точки проекта (точки удалённых/переименованных файлов не висят), затем
// нарезает и загружает чанки. Повторный прогон не дублирует векторы:
// ID чанка детерминированный, а префиксная очистка делает прогон идемпотентным.
func (c *Client) IndexProject(ctx context.Context, projectName string, files []IndexItem) (*IndexResult, error) {
	if err := c.deleteByProject(ctx, projectName); err != nil {
		return nil, err
	}

	res := &IndexResult{Files: len(files)}
	for _, f := range files {
		chunks := ChunkFile(f.Path, f.Content)
		if len(chunks) == 0 {
			continue
		}
		if err := c.upsertChunks(ctx, projectName, f.Path, f.Scope, chunks); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", f.Path, err))
			continue
		}
		res.Chunks += len(chunks)
	}
	return res, nil
}

// IndexFile переиндексирует один файл (частичная переиндексация после
// мутации, Ф-5): удаляет прежние точки файла в проекте и загружает новые.
func (c *Client) IndexFile(ctx context.Context, projectName, relPath, scope, content string) (int, error) {
	chunks := ChunkFile(relPath, content)
	if err := c.DeleteFile(ctx, projectName, relPath); err != nil {
		return 0, err
	}
	if len(chunks) == 0 {
		return 0, nil
	}
	if err := c.upsertChunks(ctx, projectName, relPath, scope, chunks); err != nil {
		return 0, err
	}
	return len(chunks), nil
}

// DeleteFile удаляет все чанки файла в проекте (удаление старого содержимого
// при переиндексации и при удалении файла из проекта).
func (c *Client) DeleteFile(ctx context.Context, projectName, relPath string) error {
	return c.deleteByFilter(ctx,
		qdrant.NewMatchKeyword(PayloadProject, projectName),
		qdrant.NewMatchKeyword(PayloadFile, relPath))
}

// deleteByProject удаляет все точки проекта (полная переиндексация).
func (c *Client) deleteByProject(ctx context.Context, projectName string) error {
	return c.deleteByFilter(ctx, qdrant.NewMatchKeyword(PayloadProject, projectName))
}

// deleteByFilter удаляет точки по фильтру (все условия — MUST).
func (c *Client) deleteByFilter(ctx context.Context, conds ...*qdrant.Condition) error {
	if len(conds) == 0 {
		return fmt.Errorf("rag: фильтр удаления пуст")
	}
	points := qdrant.NewPointsSelectorFilter(&qdrant.Filter{Must: conds})
	if _, err := c.store.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: c.collection,
		Points:         points,
	}); err != nil {
		return &UnavailableError{Err: err}
	}
	return nil
}

// upsertChunks эмбеддит чанки и грузит их пакетами (indexBatchSize). Текст
// на эмбеддинг предваряется служебной шапкой — путь и координаты файла.
func (c *Client) upsertChunks(ctx context.Context, projectName, relPath, scope string, chunks []Chunk) error {
	for start := 0; start < len(chunks); start += indexBatchSize {
		end := start + indexBatchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		points := make([]*qdrant.PointStruct, 0, end-start)
		for _, ch := range chunks[start:end] {
			vec, err := c.embed.Embed(ctx, chunkEmbedText(relPath, ch))
			if err != nil {
				return fmt.Errorf("rag: эмбеддинг %s:%d: %w", relPath, ch.StartLine, err)
			}
			points = append(points, &qdrant.PointStruct{
				Id: qdrant.NewIDNum(pointID(projectName, relPath, ch.StartLine)),
				Payload: qdrant.NewValueMap(map[string]any{
					PayloadProject:   projectName,
					PayloadFile:      relPath,
					PayloadStartLine: int64(ch.StartLine),
					PayloadEndLine:   int64(ch.EndLine),
					PayloadCode:      ch.Content,
					PayloadScope:     scope,
				}),
				Vectors: qdrant.NewVectorsDense(vec),
			})
		}
		if _, err := c.store.Upsert(ctx, &qdrant.UpsertPoints{
			CollectionName: c.collection,
			Points:         points,
		}); err != nil {
			return &UnavailableError{Err: err}
		}
	}
	return nil
}

// chunkEmbedText — текст чанка для эмбеддинга: путь и координаты добавляются
// заголовком, чтобы модель кодировала и контекст (откуда кусок), и сам код.
func chunkEmbedText(relPath string, ch Chunk) string {
	return fmt.Sprintf("файл: %s\nстроки: %d-%d\n%s", relPath, ch.StartLine, ch.EndLine, ch.Content)
}

// pointID — детерминированный ID точки чанка: FNV-1a 64 по конкатенации
// проекта, файла и начальной строки. Один и тот же чанк при повторном прогоне
// перезаписывается, а не дублируется.
func pointID(projectName, relPath string, startLine int) uint64 {
	h := fnv.New64a()
	h.Write([]byte(projectName))
	h.Write([]byte{0})
	h.Write([]byte(relPath))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(startLine)))
	return h.Sum64()
}

// ScopeForPath определяет область файла по первому сегменту пути:
// "server/internal/auth/token.go" → "server"; файлы в корне → "root".
// Область используется фильтром поиска по scope шага плана/роли агента.
func ScopeForPath(relPath string) string {
	relPath = filepath.Clean(filepath.FromSlash(relPath))
	relPath = filepath.ToSlash(relPath)
	if relPath == "." || relPath == "" {
		return "root"
	}
	if i := strings.IndexByte(relPath, '/'); i >= 0 {
		return relPath[:i]
	}
	return "root"
}