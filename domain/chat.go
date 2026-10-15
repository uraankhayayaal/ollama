package domain

// ToolDefinition описывает инструмент, доступный модели: нейтральный JSON Schema
// (без привязки к провайдеру — Ollama/OpenAI сами конвертируют его в свой формат).
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ChatRequest — нейтральный запрос к LLM на один раунд: без привязки к
// конкретному агенту. Провайдеры (Ollama/OpenAI/Layered) реализуют ChatOnce/
// ChatStream по этому запросу, а не по agents.Agent, что снимает связь
// application-слоя с infra-пакетами.
type ChatRequest struct {
	// Role — роль/имя агента, запросившего раунд: используется маршрутизацией
	// слоёв модели (эскалация small → large) и контекстом промпт-инъекций.
	Role AgentRole
	// Messages — полная история диалога раунда (без провайдер-специфичных полей).
	Messages []Message
	// Tools — инструменты, доступные модели в этом раунде.
	Tools []ToolDefinition
	// ForceToolName — имя обязательного инструмента (ToolRequiringAgent). Если
	// непусто и в истории ещё нет tool-результата, провайдер форсирует вызов
	// этого инструмента (tool_choice) и резервирует токены под большой write.
	ForceToolName string
	// Heavy — раунд требует старшего (большого) слоя модели
	// (NeedsHeavyModel/лид-роль). Провайдер-роутер учитывает флаг при выборе слоя.
	Heavy bool
}
