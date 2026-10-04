package models

import (
	"fmt"
	"strings"
)

// InjectionScope — область действия инъекции.
type InjectionScope string

const (
	ScopeGlobal    InjectionScope = "global"
	ScopeAssistant InjectionScope = "assistant"
	ScopeSession   InjectionScope = "session"
	ScopeRuntime   InjectionScope = "runtime"
)

// InjectionTarget — точка вставки инъекции в промпт.
type InjectionTarget string

const (
	TargetSystem         InjectionTarget = "system"
	TargetMessages       InjectionTarget = "messages"
	TargetUserLast       InjectionTarget = "user_last"
	TargetAssistantLast  InjectionTarget = "assistant_last"
	TargetBeforeTools    InjectionTarget = "before_tools"
	TargetAfterTools     InjectionTarget = "after_tools"
)

// InjectionPosition — способ вставки относительно целевого блока.
type InjectionPosition string

const (
	PosPrepend       InjectionPosition = "prepend"
	PosAppend        InjectionPosition = "append"
	PosReplace       InjectionPosition = "replace"
	PosInjectAtIndex InjectionPosition = "inject_at_index"
)

// Injection — структурированная промпт-инъекция, применяемая к работающей
// модели в реальном времени. Поддерживает глобальные, ассистентские,
// сессионные и runtime инъекции с чётким пайплайном приоритизации.
type Injection struct {
	ID       string            `json:"id,omitempty"`
	Name     string            `json:"name"`
	Scope    InjectionScope    `json:"scope"`
	Target   InjectionTarget   `json:"target"`
	Position InjectionPosition `json:"position"`
	Index    int               `json:"index,omitempty"`
	Content  string            `json:"content"`
	When     string            `json:"when,omitempty"`
	Enabled  bool              `json:"enabled"`
	Priority int               `json:"priority"`
	Vars     map[string]any    `json:"vars,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
}

// InjectionDefaults — дефолтные значения для полей Injection.
const (
	DefaultInjectionPosition = PosAppend
	DefaultInjectionPriority = 0
	DefaultInjectionEnabled  = true
)

// Validate проверяет корректность инъекции и возвращает ошибку.
func (inj *Injection) Validate() error {
	if strings.TrimSpace(inj.Name) == "" {
		return fmt.Errorf("injection: name не может быть пустым")
	}
	if inj.Scope == "" {
		return fmt.Errorf("injection %q: scope не может быть пустым", inj.Name)
	}
	switch inj.Scope {
	case ScopeGlobal, ScopeAssistant, ScopeSession, ScopeRuntime:
	default:
		return fmt.Errorf("injection %q: неизвестный scope %q", inj.Name, inj.Scope)
	}
	if inj.Target == "" {
		return fmt.Errorf("injection %q: target не может быть пустым", inj.Name)
	}
	switch inj.Target {
	case TargetSystem, TargetMessages, TargetUserLast, TargetAssistantLast, TargetBeforeTools, TargetAfterTools:
	default:
		return fmt.Errorf("injection %q: неизвестный target %q", inj.Name, inj.Target)
	}
	if inj.Position == "" {
		inj.Position = DefaultInjectionPosition
	}
	switch inj.Position {
	case PosPrepend, PosAppend, PosReplace, PosInjectAtIndex:
	default:
		return fmt.Errorf("injection %q: неизвестная position %q", inj.Name, inj.Position)
	}
	if inj.Position == PosInjectAtIndex && inj.Index < 0 {
		return fmt.Errorf("injection %q: inject_at_index требует index >= 0", inj.Name)
	}
	if strings.TrimSpace(inj.Content) == "" {
		return fmt.Errorf("injection %q: content не может быть пустым", inj.Name)
	}
	return nil
}

// ApplyDefaults заполняет дефолтные значения для пустых полей.
func (inj *Injection) ApplyDefaults() {
	if inj.Position == "" {
		inj.Position = DefaultInjectionPosition
	}
	if !inj.Enabled {
		inj.Enabled = DefaultInjectionEnabled
	}
	if inj.Priority == 0 {
		inj.Priority = DefaultInjectionPriority
	}
}

// Clone возвращает глубокую копию инъекции.
func (inj Injection) Clone() Injection {
	out := inj
	if inj.Vars != nil {
		out.Vars = make(map[string]any, len(inj.Vars))
		for k, v := range inj.Vars {
			out.Vars[k] = v
		}
	}
	if inj.Tags != nil {
		out.Tags = make([]string, len(inj.Tags))
		copy(out.Tags, inj.Tags)
	}
	return out
}

// ValidateInjections валидирует список инъекций, возвращает первую ошибку.
func ValidateInjections(injs []Injection) error {
	seenIDs := make(map[string]bool, len(injs))
	for i := range injs {
		inj := &injs[i]
		inj.ApplyDefaults()
		if err := inj.Validate(); err != nil {
			return err
		}
		if inj.ID != "" {
			if seenIDs[inj.ID] {
				return fmt.Errorf("injection: дубликат id %q", inj.ID)
			}
			seenIDs[inj.ID] = true
		}
	}
	return nil
}

// EnabledInjections фильтрует только активные инъекции.
func EnabledInjections(injs []Injection) []Injection {
	out := make([]Injection, 0, len(injs))
	for _, inj := range injs {
		if inj.Enabled {
			out = append(out, inj)
		}
	}
	return out
}

// ScopeOrder — порядок применения scope (от низшего к высшему).
var ScopeOrder = []InjectionScope{ScopeGlobal, ScopeAssistant, ScopeSession, ScopeRuntime}

// ScopePriority возвращает числовой приоритет scope (чем выше, тем позже применяется).
func ScopePriority(s InjectionScope) int {
	for i, sc := range ScopeOrder {
		if sc == s {
			return i
		}
	}
	return len(ScopeOrder)
}
