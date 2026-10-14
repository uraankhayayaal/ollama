package rag

// Инструмент RagIndexStatus — статус RAG-индекса проекта (см.
// PLAN-2026-09-24-done-architect-intelligence.md, Ф-1/Р-2).
//
// Модель узнаёт, построена ли векторная память проекта, чтобы решить: искать
// через CodeSearch или (при пустом индексе) читать файлы ReadMap/ReadFiles/LSP
// и предложить фоновую индексацию. Непроиндексированный проект — чёткий
// признак «indexed: false» (0 чанков), в отличие от пустой выдачи Search.
//
// Degrade: RAG опционален. Без клиента (tools.Deps.RAG == nil) или при
// недоступном Qdrant/эмбеддингах — статус skipped с подсказкой (по образцу
// CodeSearch); шаг не падает.

import (
	"context"
	"encoding/json"

	"ai/domain"
	infrarag "ai/infrastructure/rag"
)

// StatusTool — обёртка инструмента RagIndexStatus в реестре.
type StatusTool struct {
	// Searcher — клиент RAG (нил — инструмент деградирует в skipped).
	Searcher Searcher
	// Ctx — файловый контекст: имя проекта (явное или basename рабочего
	// каталога), чтобы считать чанки только своего проекта. Читается лениво.
	Ctx Context
}

func (t *StatusTool) Name() string { return IndexStatus }
func (t *StatusTool) Definition() domain.ToolDefinition {
	return domain.ToolDefinition{
		Name:        IndexStatus,
		Description: "Узнать статус векторной памяти (RAG) проекта: построен ли индекс и сколько чанков в нём. Полезно перед CodeSearch: если индекс не построен (indexed=false / status=skipped) — CodeSearch не найдёт ничего, читай файлы ReadMap/ReadFiles/LSP и предложи построить индекс.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"required":             []string{},
			"additionalProperties": false,
		},
	}
}
func (t *StatusTool) Execute(args map[string]any) ([]byte, error) { return t.exec(args) }

// exec возвращает JSON вида {"status":"ok","indexed":bool,"chunks":int}.
// Все ветки возвращают JSON, ошибка Go используется только для внутренних
// сбоев сериализации (единый стиль файловых инструментов).
func (t *StatusTool) exec(args map[string]any) ([]byte, error) {
	if t.Searcher == nil {
		return ragIndexStatusJSON(map[string]any{
			"status":  "skipped",
			"message": "векторная память RAG не подключена (нет клиента Qdrant) — используй ReadMap/ReadFiles, либо настрой QDRANT_ADDR и построй индекс командой 'go run . index <имя_проекта>'",
		}), nil
	}

	project := projectNameOf(t.Ctx)
	if project == "" {
		return ragIndexStatusJSON(map[string]any{
			"status":  "skipped",
			"message": "не определён проект (рабочий каталог не задан) — используй ReadMap/ReadFiles",
		}), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), ragSearchTimeout())
	defer cancel()

	if err := t.Searcher.Ping(ctx); err != nil {
		if infrarag.IsUnavailable(err) {
			return ragIndexStatusJSON(map[string]any{
				"status":  "skipped",
				"message": err.Error() + " — используй ReadMap/ReadFiles",
			}), nil
		}
		return ragIndexStatusJSON(map[string]any{"status": "error", "message": err.Error()}), nil
	}

	info, err := t.Searcher.ProjectInfo(ctx, project)
	if err != nil {
		if infrarag.IsUnavailable(err) {
			return ragIndexStatusJSON(map[string]any{
				"status":  "skipped",
				"message": err.Error() + " — используй ReadMap/ReadFiles",
			}), nil
		}
		return ragIndexStatusJSON(map[string]any{"status": "error", "message": err.Error()}), nil
	}

	return ragIndexStatusJSON(map[string]any{
		"status":  "ok",
		"indexed": info.Chunks > 0,
		"chunks":  info.Chunks,
	}), nil
}

// ragIndexStatusJSON сериализует результат инструмента.
func ragIndexStatusJSON(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}
