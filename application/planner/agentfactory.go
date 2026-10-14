package planner

import (
	"ai/agents"
	"ai/agents/backendlead"
	"ai/agents/developer"
	"ai/agents/devops"
	"ai/agents/devopslead"
	"ai/agents/frontendlead"
	"ai/agents/qaengineer"
	"ai/domain/services"
	"ai/infrastructure/rag"
)

// AgentFactory — порт конструирования ролевых агентов оркестратора: лидов
// направлений (Lead), специалистов по assigned_role задачи (Specialist) и
// агента шага plan-исполнителя (Step). Логика оркестрации зависит только от
// этого порта; конкретные пакеты агентов (backendlead/developer/devops/…)
// сведены в одну реализацию roleAgentFactory ниже — это инфраструктурный шов.
//
// Инверсия продолжается: в следующих срезах реализация переедет в отдельный
// инфраструктурный пакет (agents/roles) и будет внедряться в оркестратор, а
// application/planner перестанет импортировать agents/*. Пока порт внутренний:
// вызовы оркестратора уже не знают о конкретных агентах.
type AgentFactory interface {
	// Lead создаёт агента-лида направления по assigned_role эпика.
	Lead(project, role, prompt string) agents.Agent
	// Specialist создаёт агента-специалиста по assigned_role задачи.
	Specialist(project, role, prompt string) agents.Agent
	// Step создаёт агента шага plan-исполнителя по типу шага; nil — неизвестный.
	Step(step *Step, projectName string) agents.Agent
	// Architect создаёт системного архитектора (обычный режим).
	Architect(project, prompt string, store services.BoardStore) agents.Agent
	// ArchitectReviewer создаёт архитектора в режиме ревизии черновиков эпиков.
	ArchitectReviewer(project, prompt string, store services.BoardStore) agents.Agent
	// ArchitectBugExpert создаёт архитектора в режиме экспертизы багрепортов.
	ArchitectBugExpert(project, prompt string, store services.BoardStore) agents.Agent
	// ConfigureArchitect применяет к архитектору RAG и серверные инструменты
	// (Ф-4/Р-5); не-архитектор возвращается без изменений.
	ConfigureArchitect(a agents.Agent, rag rag.Searcher, extras ...services.Tool) agents.Agent
	// SetArchitectEpicWorkdir задаёт резолвер worktree ветки эпика (4.1).
	SetArchitectEpicWorkdir(a agents.Agent, fn func() (string, string)) agents.Agent
}

// infraAgentFactory — фабрика по умолчанию: ролевые агенты + архитектор.
type infraAgentFactory struct {
	roleAgentFactory
	architectFactory
}

// defaultAgentFactory — фабрика по умолчанию (реальная инфраструктура).
var defaultAgentFactory AgentFactory = infraAgentFactory{}

// SetAgentFactory переопределяет фабрику ролевых агентов (тесты, инфраструктура).
// nil восстанавливает фабрику по умолчанию.
func SetAgentFactory(f AgentFactory) {
	if f == nil {
		defaultAgentFactory = infraAgentFactory{}
		return
	}
	defaultAgentFactory = f
}

// agentFactory возвращает активную фабрику ролевых агентов.
func agentFactory() AgentFactory { return defaultAgentFactory }

// roleAgentFactory — конкретная реализация поверх пакетов агентов.
type roleAgentFactory struct{}

func (roleAgentFactory) Lead(project, role, prompt string) agents.Agent {
	switch {
	case isRole(role, "qa", "тест", "testing"):
		return qaengineer.NewQAEngineer(project, prompt)
	case isRole(role, "devops", "инфра", "infra", "dev", "sre", "ci"):
		return devopslead.NewDevopsLead(project, prompt)
	case isRole(role, "front", "react", "ui", "client", "фронт"):
		return frontendlead.NewFrontendLead(project, prompt)
	default: // backend
		return backendlead.NewBackendLead(project, prompt)
	}
}

func (roleAgentFactory) Specialist(project, role, prompt string) agents.Agent {
	switch {
	case isRole(role, "qa", "тест", "testing"):
		return qaengineer.NewQAEngineer(project, prompt)
	case isRole(role, "devops", "инфра", "infra", "sre", "docker", "k8s", "ci"):
		return devops.NewDevops(project, prompt)
	}
	// Разработка кода: задача с фронтенд-маркерами отдаётся Frontend-агенту,
	// всё остальное (включая смешанные/backend/универсальные задачи) — Backend-
	// агенту. Оба агента работают в temp/<project> в корне модуля и сами
	// создают недостающую структуру подпроекта.
	if isRole(role, "front", "react", "ui", "client", "фронт", "js", "ts", "vue") {
		return developer.NewFrontendDeveloper(project, prompt)
	}
	return developer.NewBackendDeveloper(project, prompt)
}

func (roleAgentFactory) Step(step *Step, projectName string) agents.Agent {
	switch step.Agent {
	case AgentBackendDev:
		return developer.NewBackendDeveloper(projectName, step.Prompt)
	case AgentFrontendDev:
		return developer.NewFrontendDeveloper(projectName, step.Prompt)
	case AgentDevops:
		return devops.NewDevops(projectName, step.Prompt)
	case AgentDevopsLead:
		return devopslead.NewDevopsLead(projectName, step.Prompt)
	case AgentQAEngineer:
		return qaengineer.NewQAEngineer(projectName, step.Prompt)
	case AgentFrontendLead:
		return frontendlead.NewFrontendLead(projectName, step.Prompt)
	case AgentBackendLead:
		return backendlead.NewBackendLead(projectName, step.Prompt)
	}
	return nil
}
