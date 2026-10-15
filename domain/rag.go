package domain

import (
	"path/filepath"
	"strings"
)

// Типы и хелперы векторной памяти (RAG), не зависящие от инфраструктуры.
// Конкретная реализация (Qdrant + эмбеддинги) живёт в infrastructure/rag и
// использует эти типы напрямую; порт поиска — services.Searcher.

// MainBranch — ветка по умолчанию для проектов без Git.
const MainBranch = "main"

// Лимиты выдачи семантического поиска по умолчанию (настраиваются в инструменте
// CodeSearch через RAG_MAX_RESULTS/RAG_READ_MAX_TOTAL).
const (
	// DefaultMaxResults — максимум чанков в выдаче (RAG_MAX_RESULTS).
	DefaultMaxResults = 3
	// DefaultSearchMaxTotal — суммарный лимит символов сниппетов
	// (RAG_READ_MAX_TOTAL): избыток хвоста отбрасывается.
	DefaultSearchMaxTotal = 150_000
)

// IndexOptions — ветка и версия индексации. Пустые поля трактуются как «ветка
// по умолчанию без коммита»: branch = MainBranch, commit пуст — точки просто
// заменяют друг друга (обратная совместимость для проектов без Git).
type IndexOptions struct {
	// Branch — ветка, чьи точки обновляются (main, ai/epic/ARCH-01, ...).
	Branch string
	// CommitSHA — коммит, на котором сняты чанки.
	CommitSHA string
}

// BranchOrDefault возвращает ветку из опций; пусто — MainBranch.
func (o IndexOptions) BranchOrDefault() string {
	if b := strings.TrimSpace(o.Branch); b != "" {
		return b
	}
	return MainBranch
}

// Commit возвращает обрезанный SHA коммита (пусто — версия не задана).
func (o IndexOptions) Commit() string { return strings.TrimSpace(o.CommitSHA) }

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

// SearchResult — один найденный чанк кода вместе с версионными маркерами
// (ветка/коммит/chunk_id).
type SearchResult struct {
	File      string  `json:"file"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float32 `json:"score"`
	Snippet   string  `json:"snippet"`
	// Branch — ветка, в версии которой снят чанк (main по умолчанию).
	Branch string `json:"branch,omitempty"`
	// CommitSHA — коммит, на котором проиндексирован чанк.
	CommitSHA string `json:"commit_sha,omitempty"`
	// ChunkID — стабильный идентификатор чанка (функции) между коммитами.
	ChunkID string `json:"chunk_id,omitempty"`
}

// SearchParams — параметры семантического поиска.
type SearchParams struct {
	// Project — имя проекта (фильтр payload project_name). Обязателен.
	Project string
	// Query — текст запроса.
	Query string
	// Scope — опциональный фильтр по области (payload scope, первый сегмент
	// пути: server/frontend/...). Пусто — по всему проекту.
	Scope string
	// Branch — ветка агента. Пусто — MainBranch.
	Branch string
	// Limit — максимум результатов; <=0 — DefaultMaxResults.
	Limit int
	// MaxTotal — суммарный лимит символов сниппетов; <=0 — DefaultSearchMaxTotal.
	MaxTotal int
}

// ProjectInfo — сводка индексации проекта: сколько чанков лежит в векторной
// памяти.
type ProjectInfo struct {
	// Project — имя проекта (соответствует payload project_name).
	Project string
	// Chunks — число чанков проекта в коллекции (приблизительно).
	Chunks int
}

// Indexed верно, если в индексе есть хотя бы один чанк проекта.
func (p ProjectInfo) Indexed() bool { return p.Chunks > 0 }

// ScopeForPath определяет область файла по первому сегменту пути:
// "server/internal/auth/token.go" → "server"; файлы в корне → "root".
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
