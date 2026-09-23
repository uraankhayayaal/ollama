package tools

// Инструмент RagIndexStatus — статус RAG-индекса проекта (см.
// PLAN-architect-intelligence.md, Ф-1/Р-2).
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
	"ai/rag"
	"context"
	"encoding/json"
	"time"
)

// RagIndexStatus — имя инструмента в реестре (см. registry.go newTool).
const RagIndexStatus = "RagIndexStatus"

// ragIndexStatusTool — обёртка инструмента RagIndexStatus в реестре.
type ragIndexStatusTool struct {
	// searcher — клиент RAG (нил — инструмент деградирует в skipped).
	searcher RAGSearcher
	// ops — файловый контекст: имя проекта берётся из положения OutputDir
	// (temp/<имя>), чтобы считать чанки только своего проекта.
	ops *FileOps
}

func (t *ragIndexStatusTool) Name() string { return RagIndexStatus }
func (t *ragIndexStatusTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        RagIndexStatus,
		Description: "Узнать статус векторной памяти (RAG) проекта: построен ли индекс и сколько чанков в нём. Полезно перед CodeSearch: если индекс не построен (indexed=false / status=skipped) — CodeSearch не найдёт ничего, читай файлы ReadMap/ReadFiles/LSP и предложи построить индекс.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"required":             []string{},
			"additionalProperties": false,
		},
	}
}
func (t *ragIndexStatusTool) Execute(args map[string]any) ([]byte, error) { return t.exec(args) }

// exec возвращает JSON вида {"status":"ok","indexed":bool,"chunks":int}.
// Все ветки возвращают JSON, ошибка Go используется только для внутренних
// сбоев сериализации (единый стиль файловых инструментов).
func (t *ragIndexStatusTool) exec(args map[string]any) ([]byte, error) {
	if t.searcher == nil {
		return ragIndexStatusJSON(map[string]any{
			"status":  "skipped",
			"message": "векторная память RAG не подключена (нет клиента Qdrant) — используй ReadMap/ReadFiles, либо настрой QDRANT_ADDR и построй индекс командой 'go run . index <имя_проекта>'",
		}), nil
	}

	project := projectFromOutputDir(t.ops)
	if project == "" {
		return ragIndexStatusJSON(map[string]any{
			"status":  "skipped",
			"message": "не определён проект (OutputDir не задан) — используй ReadMap/ReadFiles",
		}), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), ragSearchTimeout())
	defer cancel()

	if err := t.searcher.Ping(ctx); err != nil {
		if rag.IsUnavailable(err) {
			return ragIndexStatusJSON(map[string]any{
				"status":  "skipped",
				"message": err.Error() + " — используй ReadMap/ReadFiles",
			}), nil
		}
		return ragIndexStatusJSON(map[string]any{"status": "error", "message": err.Error()}), nil
	}

	info, err := t.searcher.ProjectInfo(ctx, project)
	if err != nil {
		if rag.IsUnavailable(err) {
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