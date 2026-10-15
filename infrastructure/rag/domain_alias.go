package rag

import (
	"ai/domain"
	"ai/domain/services"
)

// Псевдонимы доменных типов RAG: пакет rag реализует их напрямую, а прикладной
// слой (application) зависит от domain/services, а не от infrastructure/rag
// (Этап 8, срез H3b).

type (
	// IndexOptions — ветка и версия индексации.
	IndexOptions = domain.IndexOptions
	// IndexItem — один файл для полной индексации проекта.
	IndexItem = domain.IndexItem
	// IndexResult — сводка индексации проекта.
	IndexResult = domain.IndexResult
	// SearchResult — один найденный чанк кода.
	SearchResult = domain.SearchResult
	// SearchParams — параметры семантического поиска.
	SearchParams = domain.SearchParams
	// ProjectInfo — сводка индексации проекта.
	ProjectInfo = domain.ProjectInfo
	// Searcher — порт семантического поиска.
	Searcher = services.Searcher
)

const (
	// MainBranch — ветка по умолчанию для проектов без Git.
	MainBranch = domain.MainBranch
	// DefaultMaxResults — максимум чанков в выдаче (RAG_MAX_RESULTS).
	DefaultMaxResults = domain.DefaultMaxResults
	// DefaultSearchMaxTotal — суммарный лимит символов сниппетов.
	DefaultSearchMaxTotal = domain.DefaultSearchMaxTotal
)

// ScopeForPath определяет область файла по первому сегменту пути.
var ScopeForPath = domain.ScopeForPath
