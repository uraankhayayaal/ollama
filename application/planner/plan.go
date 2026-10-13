package planner

import (
	"ai/domain"
	"encoding/json"
	"fmt"
	"strings"
)

// AgentType — тип агента, которому делегируется задача. Тип принадлежит
// домену (domain.AgentType), здесь — алиас для совместимости кода плана.
type AgentType = domain.AgentType

const (
	// AgentBackendDev — агент «Backend разработчик»: серверная часть проекта.
	AgentBackendDev = domain.AgentBackendDev
	// AgentFrontendDev — агент «Frontend разработчик»: клиентская часть проекта.
	AgentFrontendDev  = domain.AgentFrontendDev
	AgentDevops       = domain.AgentDevops
	AgentDevopsLead   = domain.AgentDevopsLead
	AgentQAEngineer   = domain.AgentQAEngineer
	AgentFrontendLead = domain.AgentFrontendLead
	AgentBackendLead  = domain.AgentBackendLead
)

// Role — роль разработчика. Задаётся планировщиком, когда единый этап
// генерации/доработки нужно выполнить под стек конкретного подпроекта:
// фронтенд или бэкенд. Для шагов с отдельным агентом (AgentBackendDev/
// AgentFrontendDev) роль фиксирована самим типом агента и хранится только
// для журналирования (agentLabel); для шагов с generic-типами роль может
// влиять на выбор агента исполнением. Тип принадлежит домену (domain.Role).
type Role = domain.Role

const (
	RoleFrontend = domain.RoleFrontend
	RoleBackend  = domain.RoleBackend
)

// agentDisplayName — человекочитаемое имя агента по типу шага. Используется
// в префиксах логов (вместе с ролью — см. agentLabel).
func agentDisplayName(a AgentType) string {
	switch a {
	case AgentBackendDev:
		return "бэкенд-разработчик"
	case AgentFrontendDev:
		return "фронтенд-разработчик"
	case AgentDevops:
		return "DevOps-инженер"
	case AgentDevopsLead:
		return "DevOps-лид"
	case AgentQAEngineer:
		return "QA-инженер"
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

// Step — один этап плана, делегируемый конкретному агенту. Тип принадлежит
// домену (domain.Step), здесь — алиас; ComputeWaves — метод domain.Plan.
type Step = domain.Step

// Plan — структурированный план, возвращаемый планировщиком. Тип принадлежит
// домену (domain.Plan).
type Plan = domain.Plan

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
