package planner

import (
	"ai/domain/services"
)

// LLMProvider — порт полного агентского цикла для оркестратора плана.
// Определён в domain/services (services.AgentCycle) и реализуется
// application-слоем (usecases.FullCycle); planner зависит только от порта, а
// не от пакета llm или конкретного провайдера.
type LLMProvider = services.AgentCycle
