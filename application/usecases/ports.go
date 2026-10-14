package usecases

import (
	"ai/agents"
	"ai/domain"
	"context"
	"fmt"
)

// ChatProvider — провайдер, умеющий сделать ОДИН запрос к модели по
// нейтральному domain.ChatRequest (без привязки к agents.Agent).
type ChatProvider interface {
	ChatOnce(ctx context.Context, req domain.ChatRequest) (*domain.ModelReply, error)
}

// StreamChatProvider — опциональное расширение ChatProvider: отдаёт текст
// ответа по кускам через onChunk.
type StreamChatProvider interface {
	ChatStream(ctx context.Context, req domain.ChatRequest, onChunk func(domain.StreamChunk)) (*domain.ModelReply, error)
}

// ModelLimitsProvider — опциональный интерфейс провайдера: сообщает лимиты
// модели до запроса (для упреждающего сжатия контекста).
type ModelLimitsProvider interface {
	ModelLimits() domain.ModelLimits
}

// Reporter — порт трансляции live-событий агентского цикла (Web UI). Реализуется
// runevents.Router в инфраструктуре; use-cases зависит только от этого набора.
type Reporter interface {
	OnMessage(role, content string, truncated bool)
	OnMessageDelta(streamID, content string)
	OnToolStart(tool, args string)
	OnToolResult(tool, result string, ok bool)
	OnTokens(in, out int64, tps float64)
}

// Agent — доменный порт агента, запускаемого в цикле: промпты, доступные
// инструменты и диспетчер вызовов. agents.Agent структурно удовлетворяет ему
// (за счёт алиасов domain.AgentMessage/domain.ToolDefinition).
type Agent interface {
	GetSystemMessages(text []domain.AgentMessage) []domain.AgentMessage
	GetUserMessages() []domain.AgentMessage
	GetTools() []domain.ToolDefinition
	CallFunction(functionName string, functionArgs map[string]any) ([]byte, error)
}

// ToolRequiringAgent — необязательный интерфейс агента: имя инструмента,
// обязательного к успешному вызову за цикл.
type ToolRequiringAgent interface {
	RequiredToolFirstRound() (string, bool)
}

// RequiredToolGroupsAgent — необязательный интерфейс агента: группы
// инструментов, из каждой должен успешно выполниться хотя бы один.
type RequiredToolGroupsAgent interface {
	RequiredToolGroups() [][]string
}

// AutoFixer — необязательный интерфейс агента: очередь файлов, затронутых
// мутациями, и LSP-диагностика по явному списку файлов.
type AutoFixer interface {
	TakeTouched() []string
	LspCheckFiles(files []string) (diagnostics []string, hadMutation bool)
}

// Reindexer — необязательный интерфейс агента: частичная переиндексация RAG
// по списку затронутых файлов.
type Reindexer interface {
	ReindexTouched(touched []string) (reindexed int, err error)
}

// WIPCommitter — необязательный интерфейс агента: промежуточный коммит
// изменений раунда (единица ручного отката).
type WIPCommitter interface {
	CommitRoundTouched(touched []string, round int) (sha string, committed bool, err error)
}

// RuntimeLogger — необязательный интерфейс агента: дренирование логов
// рантайма приложения для цикла самокоррекции.
type RuntimeLogger interface {
	TakeAppLogTail() (source string, lines []string)
}

// RoundStateReporter — необязательный интерфейс агента: запись состояния
// раунда (проверка упала/идёт, SHA промежуточного коммита).
type RoundStateReporter interface {
	ReportRoundState(domain.RoundState)
}

// AgentRunner — порт агентского раннера: выполняет полный агентский цикл
// (диалог + инструменты) для конкретного провайдера и агента. Реализуется
// инфраструктурой (runner).
type AgentRunner interface {
	Generate(ctx context.Context, provider ChatProvider, agent agents.Agent) (*domain.AgentResponse, error)
}

// agentRunner — подключённая реализация AgentRunner (инфраструктура).
var agentRunner AgentRunner

// SetAgentRunner подключает реализацию агентского раннера.
func SetAgentRunner(r AgentRunner) { agentRunner = r }

// RunAgent запускает полный агентский цикл через подключённый раннер.
func RunAgent(ctx context.Context, provider ChatProvider, agent agents.Agent) (*domain.AgentResponse, error) {
	if agentRunner == nil {
		return nil, fmt.Errorf("агентский раннер не подключён (usecases.SetAgentRunner)")
	}
	return agentRunner.Generate(ctx, provider, agent)
}
