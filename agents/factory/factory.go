// Package factory — фабрика агентов: ролевые специалисты и инструменты.
// Живёт в outer-слое (agents), поэтому вправе импортировать concrete agents/*;
// установка — в composition root (main.go) или server.
package factory

import (
	"ai/agents"
	"ai/agents/developer"
	"ai/tools"
	"strings"
)

// toolRemover — интерфейс агентов с набором инструментов, допускающим удаление.
type toolRemover interface {
	ToolsSet() *tools.Set
}

// Factory — фабрика: ролевые агенты (backend/frontend).
type Factory struct {
	roleFactory
}

// New возвращает фабрику ролевых агентов.
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
	// Разработка кода: задача с фронтенд-маркерами отдаётся Frontend-агенту,
	// всё остальное (включая backend/универсальные задачи) — Backend-агенту.
	if isRole(role, "front", "react", "ui", "client", "фронт", "js", "ts", "vue") {
		return developer.NewFrontendDeveloper(project, prompt)
	}
	return developer.NewBackendDeveloper(project, prompt)
}

func (roleFactory) Step(agentType string, projectName, prompt string) agents.Agent {
	switch agentType {
	case "backend":
		return developer.NewBackendDeveloper(projectName, prompt)
	case "frontend":
		return developer.NewFrontendDeveloper(projectName, prompt)
	}
	return nil
}

// RemoveToolsFromAgent удаляет инструменты из агента по именам.
func (roleFactory) RemoveToolsFromAgent(a agents.Agent, names []string) agents.Agent {
	tr, ok := a.(toolRemover)
	if !ok {
		return a
	}
	for _, n := range names {
		tr.ToolsSet().Remove(n)
	}
	return a
}

// ConfigureAgent — заглушка (раньше настраивал архитектора).
func (roleFactory) ConfigureAgent(a agents.Agent, searcher any, extras ...any) agents.Agent {
	return a
}
