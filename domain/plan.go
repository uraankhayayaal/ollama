package domain

import (
	"fmt"
	"sort"
)

// AgentType — тип ролевого агента, которому делегируется шаг плана.
// Значения канонические: совпадают с сериализуемым в чекпоинтах/PLAN.md видом
// (planner исторически использовал именно их; ранее здесь были расходящиеся
// «backend_dev»/«qa_engineer» и т.п. — исправлено при переносе типов в domain).
type AgentType string

const (
	AgentBackendDev   AgentType = "backend"
	AgentFrontendDev  AgentType = "frontend"
	AgentDevops       AgentType = "devops"
	AgentDevopsLead   AgentType = "devopslead"
	AgentQAEngineer   AgentType = "qa"
	AgentFrontendLead AgentType = "frontendlead"
	AgentBackendLead  AgentType = "backendlead"
	// Типы вне ролей плана (архитектор и вспомогательные агенты).
	AgentArchitect    AgentType = "architect"
	AgentPromptCheck  AgentType = "prompt_check"
	AgentCodeReviewer AgentType = "code_reviewer"
	AgentChatAssist   AgentType = "chat_assist"
)

type Role string

const (
	RoleFrontend Role = "frontend"
	RoleBackend  Role = "backend"
)

// Step — один этап плана, делегируемый конкретному агенту.
type Step struct {
	ID          string    `json:"id"`
	Agent       AgentType `json:"agent"`
	Prompt      string    `json:"prompt"`
	ProjectName string    `json:"project_name,omitempty"`
	DependsOn   []string  `json:"depends_on,omitempty"`
	Description string    `json:"description"`
	// Scope — область работы агента: конкретные файлы и/или директории
	// проекта, на которых сосредоточен этот шаг (например "src/main.go"
	// или "internal/order/"). Пустой — агент работает со всем проектом.
	Scope []string `json:"scope,omitempty"`
	// Role — роль разработчика (frontend/backend). Задаётся планировщиком,
	// когда единая работа делится на фронтенд и бэкенд. Для шагов с типом
	// AgentBackendDev/AgentFrontendDev роль уже зафиксирована агентом и
	// хранится только для журналирования.
	Role Role `json:"role,omitempty"`
	// Tasks — подзадачи, декомпозированные лидом направления для этого шага
	// (JSON-декомпозиция лида в plan-режиме). Заполняются исполнителем при
	// выполнении лид-шага и сериализуются в чекпоинт вместе с планом, чтобы
	// PLAN.md переживал resume. Каждая задача несёт полный контракт в
	// Description (типы, интерфейсы, сигнатуры методов, API).
	Tasks []TaskSpec `json:"tasks,omitempty"`
}

// Plan — структурированный план, возвращаемый планировщиком.
type Plan struct {
	ProjectName string `json:"project_name"`
	Summary     string `json:"summary"`
	Steps       []Step `json:"steps"`
}

// ComputeWaves группирует шаги плана по волнам параллельного выполнения.
// Внутри одной волны шаги НЕ зависят друг от друга (их зависимости уже
// удовлетворены), поэтому их можно запускать параллельно. Волны идут строго
// последовательно: волна N+1 стартует только после завершения всех шагов
// волны N. Возвращает ошибку при циклических зависимостях или отсутствии шагов.
func (p *Plan) ComputeWaves() ([][]string, error) {
	inDegree := make(map[string]int)
	dependents := make(map[string][]string)

	for _, step := range p.Steps {
		if _, ok := inDegree[step.ID]; !ok {
			inDegree[step.ID] = 0
		}
		for _, dep := range step.DependsOn {
			dependents[dep] = append(dependents[dep], step.ID)
			inDegree[step.ID]++
		}
	}

	var waves [][]string
	processed := 0

	// Текущая волна — шаги, у которых все зависимости выполнены.
	// Детерминированный (отсортированный) порядок важен для чекпоинтов,
	// чтобы resume не зависел от порядка обхода карты.
	current := readySteps(inDegree)
	for len(current) > 0 {
		waves = append(waves, current)
		processed += len(current)

		var next []string
		for _, id := range current {
			for _, dep := range dependents[id] {
				inDegree[dep]--
				if inDegree[dep] == 0 {
					next = append(next, dep)
				}
			}
		}
		current = next
	}

	if processed != len(p.Steps) {
		return nil, fmt.Errorf("обнаружен цикл в зависимостях плана")
	}
	if len(p.Steps) > 0 && len(waves) == 0 {
		return nil, fmt.Errorf("план не содержит шагов")
	}
	return waves, nil
}

// readySteps возвращает шаги с нулевой степенью входа в отсортированном
// порядке (детерминизм волн и чекпоинтов).
func readySteps(inDegree map[string]int) []string {
	ids := make([]string, 0, len(inDegree))
	for id, deg := range inDegree {
		if deg == 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
