// Статус RAG-индекса проекта: сколько чанков проекта лежит в векторной
// памяти (см. PLAN-architect-intelligence.md, Ф-1, Р-2).
//
// Непроиндексированный проект — 0 чанков (чёткий признак, в отличие от пустой
// выдачи Search). Подсчёт точечный по payload project_name через Count API
// Qdrant; count/файловый список приблизительные. Коллекции нет — 0 без ошибки.

package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/qdrant/go-client/qdrant"
)

// ProjectInfo — сводка индексации проекта: сколько чанков лежит в векторной
// памяти. Используется инструментом RagIndexStatus для принятия решения
// «индекс построен или нет» (0 чанков — не проиндексирован).
type ProjectInfo struct {
	// Project — имя проекта (соответствует payload project_name).
	Project string
	// Chunks — число чанков проекта в коллекции (приблизительно).
	Chunks int
}

// Indexed верно, если в индексе есть хотя бы один чанк проекта.
func (p ProjectInfo) Indexed() bool { return p.Chunks > 0 }

// ProjectInfo считает чанки проекта в векторной памяти (Count API с фильтром
// по project_name). Коллекция ещё не создана — 0 чанков, без ошибки.
// Недоступный Qdrant оборачивается в UnavailableError.
func (c *Client) ProjectInfo(ctx context.Context, projectName string) (ProjectInfo, error) {
	if strings.TrimSpace(projectName) == "" {
		return ProjectInfo{}, fmt.Errorf("rag: пустое имя проекта для подсчёта индекса")
	}

	exists, err := c.store.CollectionExists(ctx, c.collection)
	if err != nil {
		return ProjectInfo{}, &UnavailableError{Err: err}
	}
	if !exists {
		return ProjectInfo{Project: projectName}, nil
	}

	resp, err := c.store.Count(ctx, &qdrant.CountPoints{
		CollectionName: c.collection,
		Filter: &qdrant.Filter{Must: []*qdrant.Condition{
			qdrant.NewMatchKeyword(PayloadProject, projectName),
		}},
		// Точный подсчёт: коллекции проектов небольшие, approximate дал бы
		// плавающий «индекс построен/нет» на границе нуля.
		Exact: qdrant.PtrOf(true),
	})
	if err != nil {
		return ProjectInfo{}, &UnavailableError{Err: err}
	}
	n := 0
	if resp.GetResult() != nil {
		n = int(resp.GetResult().GetCount())
	}
	return ProjectInfo{Project: projectName, Chunks: n}, nil
}