package planner

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// AgentType — тип агента, которому делегируется задача.
type AgentType string

const (
	// AgentBackendDev — агент «Backend разработчик»: серверная часть проекта.
	AgentBackendDev AgentType = "backend"
	// AgentFrontendDev — агент «Frontend разработчик»: клиентская часть проекта.
	AgentFrontendDev  AgentType = "frontend"
	AgentCodeReviewer AgentType = "codereviewer"
	AgentAcceptor     AgentType = "acceptor"
	AgentDevops       AgentType = "devops"
	AgentDevopsLead   AgentType = "devops-lead"
	AgentQAEngineer   AgentType = "qa"
	AgentQALead       AgentType = "qalead"
	AgentFrontendLead AgentType = "frontendlead"
	AgentBackendLead  AgentType = "backendlead"
)

// Role — роль разработчика. Задаётся планировщиком, когда единый этап
// генерации/доработки нужно выполнить под стек конкретного подпроекта:
// фронтенд или бэкенд. Для шагов с отдельным агентом (AgentBackendDev/
// AgentFrontendDev) роль фиксирована самим типом агента и хранится только
// для журналирования (agentLabel); для шагов с generic-типами роль может
// влиять на выбор агента исполнением.
type Role string

const (
	RoleFrontend Role = "frontend"
	RoleBackend  Role = "backend"
)

// agentDisplayName — человекочитаемое имя агента по типу шага. Используется
// в префиксах логов (вместе с ролью — см. agentLabel).
func agentDisplayName(a AgentType) string {
	switch a {
	case AgentBackendDev:
		return "бэкенд-разработчик"
	case AgentFrontendDev:
		return "фронтенд-разработчик"
	case AgentCodeReviewer:
		return "код-ревьювер"
	case AgentAcceptor:
		return "приёмка"
	case AgentDevops:
		return "DevOps-инженер"
	case AgentDevopsLead:
		return "DevOps-лид"
	case AgentQAEngineer:
		return "QA-инженер"
	case AgentQALead:
		return "QA-лид"
	case AgentFrontendLead:
		return "Frontend-лид"
	case AgentBackendLead:
		return "Backend-лид"
	default:
		return string(a)
	}
}

// agentLabel — префикс лога «имя агента / его роль» для шага плана.
// Роль разработчика (frontend/backend) добавляется через слэш.
func agentLabel(a AgentType, role Role) string {
	name := agentDisplayName(a)
	if role != "" {
		name += "/" + string(role)
	}
	return name
}

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
}

// Plan — структурированный план, возвращаемый планировщиком.
type Plan struct {
	ProjectName string `json:"project_name"`
	Summary     string `json:"summary"`
	Steps       []Step `json:"steps"`
}

// ParsePlan разбирает JSON-ответ планировщика в Plan.
// Если модель вернала текст без JSON — возвращает ошибку.
func ParsePlan(data string) (*Plan, error) {
	data = extractJSON(data)
	var p Plan
	if err := json.Unmarshal([]byte(sanitizeJSON(data)), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ComputeWaves группирует шаги плана по волнам параллельного выполнения.
// Внутри одной волны шаги НЕ зависят друг от друга (их зависимости уже
// удовлетворены), поэтому их можно запускать параллельно. Волны идут строго
// последовательно: волна N+1 стартует только после завершения всех шагов
// волны N.
//
// Это «задел на будущее» для распараллеливания задач: сейчас исполнитель
// прогоняет шаги волны последовательно, но структура уже готова к тому,
// чтобы каждый шаг волны выполнялся в отдельной горутине/агенте.
//
// Возвращает ошибку при циклических зависимостях или отсутствии шагов.
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

// sanitizeJSON чинит типовые ошибки модели: внутри JSON-строк («prompt»,
// «summary») модель вставляет реальные управляющие символы — переводы строк
// и возвраты каретки, которые в JSON обязаны быть экранированы (\n, \r).
// Иначе json.Unmarshal падает с «invalid character '\r' in string literal».
// Заменяем каждый управляющий символ внутри строки на его \uXXXX-эскейп
// (эквивалентно \n/\r/\t по спецификации JSON), а вне строк \r просто
// убираем (там допустимы только пробел, \t, \n).
func sanitizeJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escaped := false
	flush := func() {
		inString = false
		escaped = false
	}
	defer flush()

	for _, r := range s {
		if inString {
			switch {
			case escaped:
				b.WriteRune(r)
				escaped = false
			case r == '\\':
				b.WriteRune(r)
				escaped = true
			case r == '"':
				b.WriteRune(r)
				inString = false
			case r < 0x20:
				// Управляющий символ внутри строки — экранируем.
				fmt.Fprintf(&b, "\\u%04x", r)
			default:
				b.WriteRune(r)
			}
			continue
		}
		switch r {
		case '"':
			b.WriteRune(r)
			inString = true
		case '\r':
			// Между токенами невидимый \r ни на что не влияет — пропускаем.
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// extractJSON пытается найти JSON-объект в тексте модели:
// ищет первый '{' и последний '}', чтобы отбросить markdown-обёртки.
func extractJSON(s string) string {
	start := -1
	for i, c := range s {
		if c == '{' {
			start = i
			break
		}
	}
	if start < 0 {
		return s
	}
	end := -1
	for i := len(s) - 1; i >= start; i-- {
		if s[i] == '}' {
			end = i
			break
		}
	}
	if end < 0 {
		return s
	}
	return s[start : end+1]
}
