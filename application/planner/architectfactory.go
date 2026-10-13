package planner

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/board"
	"ai/tools"
)

// architectFactory — архитектурная часть AgentFactory поверх agents/architect.
// Фабрика возвращает конкретный *architect.Architect как agents.Agent: динамический
// тип агента сохраняется (важно для тестовых двойников, различающих архитектора
// по типу), а оркестратор при этом не импортирует пакет agents/architect.
type architectFactory struct{}

func (architectFactory) Architect(project, prompt string, store *board.Store) agents.Agent {
	return architect.NewArchitectWithStore(project, prompt, store)
}

func (architectFactory) ArchitectReviewer(project, prompt string, store *board.Store) agents.Agent {
	return architect.NewArchitectWithStore(project, prompt, store).AsReviewer()
}

func (architectFactory) ArchitectBugExpert(project, prompt string, store *board.Store) agents.Agent {
	return architect.NewArchitectWithStore(project, prompt, store).AsBugExpert()
}

func (architectFactory) ConfigureArchitect(a agents.Agent, rag tools.RAGSearcher, extras ...tools.Tool) agents.Agent {
	arch, ok := a.(*architect.Architect)
	if !ok {
		return a
	}
	if rag != nil {
		arch.SetRAG(rag)
	}
	if len(extras) > 0 {
		arch.Tools.Add(extras...)
	}
	return arch
}

func (architectFactory) SetArchitectEpicWorkdir(a agents.Agent, fn func() (string, string)) agents.Agent {
	if arch, ok := a.(*architect.Architect); ok {
		arch.SetEpicWorkdir(fn)
	}
	return a
}
