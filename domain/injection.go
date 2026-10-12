package domain

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	InjectionScopeGlobal    = "global"
	InjectionScopeAssistant = "assistant"
	InjectionScopeSession   = "session"
	InjectionScopeRuntime   = "runtime"
	InjectionScopeTask      = "task"
)

const (
	InjectionTargetSystem        = "system"
	InjectionTargetMessages      = "messages"
	InjectionTargetUserLast      = "user_last"
	InjectionTargetAssistantLast = "assistant_last"
)

const (
	InjectionPosPrepend       = "prepend"
	InjectionPosAppend        = "append"
	InjectionPosReplace       = "replace"
	InjectionPosInjectAtIndex = "inject_at_index"
)

const DefaultInjectionPosition = InjectionPosAppend
const MaxInjectionContentSize = 10 * 1024

type Injection struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Scope    string         `json:"scope,omitempty"`
	Target   string         `json:"target"`
	Position string         `json:"position,omitempty"`
	Index    int            `json:"index,omitempty"`
	Content  string         `json:"content"`
	When     string         `json:"when,omitempty"`
	Enabled  *bool          `json:"enabled,omitempty"`
	Priority int            `json:"priority"`
	Vars     map[string]any `json:"vars,omitempty"`
	Tags     []string       `json:"tags,omitempty"`
}

func (inj Injection) IsEnabled() bool { return inj.Enabled == nil || *inj.Enabled }

func (inj *Injection) SetEnabled(v bool) { inj.Enabled = &v }

func (inj Injection) EffectiveScope() string {
	if inj.Scope == InjectionScopeTask {
		return InjectionScopeRuntime
	}
	return inj.Scope
}

func (inj *Injection) Normalize() {
	inj.Scope = inj.EffectiveScope()
	if inj.Scope == "" {
		inj.Scope = InjectionScopeRuntime
	}
	if inj.Position == "" {
		inj.Position = DefaultInjectionPosition
	}
	if inj.ID == "" {
		inj.ID = generateInjectionID()
	}
}

func generateInjectionID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(i) + 1
		}
	}
	return "inj-" + hex.EncodeToString(b)
}

func ValidateInjections(injs []Injection) error {
	for i := range injs {
		injs[i].Normalize()
		if injs[i].Name == "" {
			return fmt.Errorf("injection %d: пустое имя", i)
		}
		switch injs[i].Target {
		case InjectionTargetSystem, InjectionTargetMessages, InjectionTargetUserLast, InjectionTargetAssistantLast:
		default:
			return fmt.Errorf("injection %s: неизвестный target %q", injs[i].Name, injs[i].Target)
		}
		if injs[i].Content == "" {
			return fmt.Errorf("injection %s: пустой контент", injs[i].Name)
		}
		if len(injs[i].Content) > MaxInjectionContentSize {
			return fmt.Errorf("injection %s: контент превышает %d байт", injs[i].Name, MaxInjectionContentSize)
		}
	}
	return nil
}

func DecodeInjections(data any) ([]Injection, error) {
	var raw []byte
	switch v := data.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return nil, fmt.Errorf("injections: неподдерживаемый тип %T", data)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var injs []Injection
	if err := json.Unmarshal(raw, &injs); err != nil {
		return nil, err
	}
	return injs, nil
}
