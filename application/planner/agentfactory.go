package planner

import (
	"ai/domain"
	"ai/domain/services"
	"ai/tools"
)

// AgentFactory — порт конструирования ролевых агентов оркестратора: специалистов
// по assigned_role задачи (Specialist), агента шага plan-исполнителя (Step),
// системного архитектора и бизнес-аналитика. Логика оркестрации зависит только
// от этого порта; конкретные пакеты агентов (developer/devops/architect/…)
// сведены в реализацию внешнего пакета `ai/agents/factory`, которую устанавливает
// composition root (`main.go`) через SetAgentFactory. Так application/planner
// не импортирует agents/* — направление зависимости соблюдено.
//
// `Step` намеренно принимает доменный AgentType, а не *Step, чтобы реализация
// фабрики не зависела от этого пакета (иначе получился бы цикл импорта
// planner → agents/factory → planner).
// DecomposeToolRemoval — имена инструментов, которые исключаются из набора
// архитектора при декомпозиции эпика: архитектор создаёт только задачи,
// а не под-эпики (Fix #2).
var DecomposeToolRemoval = []string{
	tools.BoardCreateEpic,
}

type AgentFactory interface {
	// Specialist создаёт агента-специалиста по assigned_role задачи.
	Specialist(project, role, prompt string) services.Agent
	// Step создаёт агента шага plan-исполнителя по типу шага; nil — неизвестный.
	Step(agentType domain.AgentType, projectName, prompt string) services.Agent
	// Architect создаёт системного архитектора (обычный режим).
	Architect(project, prompt string, store services.BoardStore) services.Agent
	// ArchitectReviewer создаёт архитектора в режиме ревизии черновиков эпиков.
	ArchitectReviewer(project, prompt string, store services.BoardStore) services.Agent
	// ConfigureArchitect применяет к архитектору RAG и серверные инструменты
	// (Ф-4/Р-5); не-архитектор возвращается без изменений.
	ConfigureArchitect(a services.Agent, rag services.Searcher, extras ...services.Tool) services.Agent
	// SetArchitectEpicWorkdir задаёт резолвер worktree ветки эпика (4.1).
	SetArchitectEpicWorkdir(a services.Agent, fn func() (string, string)) services.Agent
	// RemoveToolsFromAgent исключает инструменты из набора агента.
	// Используется для снятия BoardCreateEpic при декомпозиции (Fix #2).
	// Не-поддерживаемый агент возвращается без изменений.
	RemoveToolsFromAgent(a services.Agent, names []string) services.Agent
	// BusinessAnalyst создаёт бизнес-аналитика для фазы business_review эпика.
	BusinessAnalyst(project, prompt string, store services.BoardStore) services.Agent
}

// noopAgentFactory — фабрика по умолчанию, пока composition root не установил
// реальную (`agents/factory`). Возвращает nil-агентов: запуск агента без
// установленной фабрики — ошибка сборки, а не молчаливое поведение.
type noopAgentFactory struct{}

func (noopAgentFactory) Specialist(string, string, string) services.Agent { return nil }
func (noopAgentFactory) Step(domain.AgentType, string, string) services.Agent {
	return nil
}
func (noopAgentFactory) Architect(string, string, services.BoardStore) services.Agent {
	return nil
}
func (noopAgentFactory) ArchitectReviewer(string, string, services.BoardStore) services.Agent {
	return nil
}
func (noopAgentFactory) ConfigureArchitect(a services.Agent, _ services.Searcher, _ ...services.Tool) services.Agent {
	return a
}
func (noopAgentFactory) SetArchitectEpicWorkdir(a services.Agent, _ func() (string, string)) services.Agent {
	return a
}
func (noopAgentFactory) RemoveToolsFromAgent(a services.Agent, _ []string) services.Agent {
	return a
}
func (noopAgentFactory) BusinessAnalyst(string, string, services.BoardStore) services.Agent {
	return nil
}

// defaultAgentFactory — фабрика по умолчанию (устанавливается в main.go).
var defaultAgentFactory AgentFactory = noopAgentFactory{}

// SetAgentFactory переопределяет фабрику ролевых агентов (тесты, инфраструктура).
// nil возвращает заглушку-пустышку.
func SetAgentFactory(f AgentFactory) {
	if f == nil {
		defaultAgentFactory = noopAgentFactory{}
		return
	}
	defaultAgentFactory = f
}

// agentFactory возвращает активную фабрику ролевых агентов.
func agentFactory() AgentFactory { return defaultAgentFactory }
