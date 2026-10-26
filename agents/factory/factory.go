// Package factory — реализация порта planner.AgentFactory поверх конкретных
// пакетов агентов. Живёт в outer-слое (agents), поэтому вправе импортировать
// concrete agents/*; application/planner от них не зависит (RAG-зависимость
// выражена портом domain/services.Searcher). Установка — в composition root
// (main.go): planner.SetAgentFactory(factory.New()).
package factory

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/agents/businessanalyst"
	"ai/agents/developer"
	"ai/agents/devops"
	"ai/agents/qaengineer"
	"ai/domain"
	"ai/domain/services"
	"strings"
)

// Factory — фабрика по умолчанию: ролевые агенты + архитектор + бизнес-аналитик.
type Factory struct {
	roleFactory
	architectFactory
	businessAnalystFactory
}

// New возвращает фабрику ролевых агентов и архитектора.
func New() *Factory { return &Factory{} }

// isRole проверяет, содержит ли роль (в нижнем регистре) один из маркеров.
func isRole(role string, markers ...string) bool {
	role = strings.ToLower(role)
	for _, m := range markers {
		if strings.Contains(role, m) {
			return true
		}
	}
	return false
}

// roleFactory — конкретная реализация ролевых агентов поверх пакетов агентов.
type roleFactory struct{}

func (roleFactory) Specialist(project, role, prompt string) agents.Agent {
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

func (roleFactory) Step(agentType domain.AgentType, projectName, prompt string) agents.Agent {
	switch agentType {
	case domain.AgentBackendDev:
		return developer.NewBackendDeveloper(projectName, prompt)
	case domain.AgentFrontendDev:
		return developer.NewFrontendDeveloper(projectName, prompt)
	case domain.AgentDevops:
		return devops.NewDevops(projectName, prompt)
	case domain.AgentQAEngineer:
		return qaengineer.NewQAEngineer(projectName, prompt)
	case domain.AgentBusinessAnalyst:
		return businessanalyst.NewBusinessAnalyst(projectName, prompt)
	}
	return nil
}

// architectFactory — архитектурная часть поверх agents/architect. Возвращает
// конкретный *architect.Architect как agents.Agent: динамический тип агента
// сохраняется (важно для тестовых двойников, различающих архитектора по типу).
type architectFactory struct{}

func (architectFactory) Architect(project, prompt string, store services.BoardStore) agents.Agent {
	return architect.NewArchitectWithStore(project, prompt, store)
}

func (architectFactory) ArchitectReviewer(project, prompt string, store services.BoardStore) agents.Agent {
	return architect.NewArchitectWithStore(project, prompt, store).AsReviewer()
}

func (architectFactory) ConfigureArchitect(a agents.Agent, searcher services.Searcher, extras ...services.Tool) agents.Agent {
	arch, ok := a.(*architect.Architect)
	if !ok {
		return a
	}
	if searcher != nil {
		arch.SetRAG(searcher)
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

// businessAnalystFactory — бизнес-аналитик поверх agents/businessanalyst.
type businessAnalystFactory struct{}

func (businessAnalystFactory) BusinessAnalyst(project, prompt string, store services.BoardStore) agents.Agent {
	ba := businessanalyst.NewBusinessAnalyst(project, prompt)
	if store != nil {
		ba.SetBoardStore(store)
	}
	return ba
}
