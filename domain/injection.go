package domain

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
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
		inj.ID = NewInjectionID()
	}
}

// NewInjectionID — новый идентификатор инъекции (16 байт энтропии в hex).
func NewInjectionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand на поддерживаемых платформах ошибок не возвращает.
		panic("board: crypto/rand недоступен: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Validate проверяет структурную корректность инъекции. Unknown-поля сюда не
// попадают: их отсекает строгий декодер на стороне API.
//
// Пустые scope и position допустимы и трактуются как дефолты (runtime /
// append) — иначе минимальная запись {"name","target","content"} была бы
// невалидной, хотя ровно её и просят в API.
func (inj Injection) Validate() error {
	if strings.TrimSpace(inj.Name) == "" {
		return fmt.Errorf("инъекция: имя (name) обязательно")
	}
	if strings.TrimSpace(inj.Content) == "" {
		return fmt.Errorf("инъекция %q: тело (content) обязательно", inj.Name)
	}
	if len(inj.Content) > MaxInjectionContentSize {
		return fmt.Errorf("инъекция %q: content %d байт больше лимита %d",
			inj.Name, len(inj.Content), MaxInjectionContentSize)
	}
	switch inj.EffectiveScope() {
	case "", InjectionScopeGlobal, InjectionScopeAssistant, InjectionScopeSession, InjectionScopeRuntime:
	default:
		return fmt.Errorf("инъекция %q: неизвестный scope %q", inj.Name, inj.Scope)
	}
	switch inj.Target {
	case InjectionTargetSystem, InjectionTargetMessages, InjectionTargetUserLast, InjectionTargetAssistantLast:
	default:
		return fmt.Errorf("инъекция %q: неизвестный target %q", inj.Name, inj.Target)
	}
	position := inj.Position
	if position == "" {
		position = DefaultInjectionPosition
	}
	switch position {
	case InjectionPosPrepend, InjectionPosAppend, InjectionPosReplace, InjectionPosInjectAtIndex:
	default:
		return fmt.Errorf("инъекция %q: неизвестная position %q", inj.Name, inj.Position)
	}
	if position == InjectionPosInjectAtIndex && inj.Index < 0 {
		return fmt.Errorf("инъекция %q: inject_at_index требует index >= 0", inj.Name)
	}
	return nil
}

// ValidateInjections нормализует и проверяет список: дефолты, структура,
// уникальность id. Мутирует элементы (нормализация) и возвращает первую
// ошибку — вызывающий код отвечает пользователю 400, а не молча пишет мусор.
func ValidateInjections(injs []Injection) error {
	seen := make(map[string]int, len(injs))
	for i := range injs {
		inj := &injs[i]
		inj.Normalize()
		if err := inj.Validate(); err != nil {
			return err
		}
		if inj.ID == "" {
			continue
		}
		if prev, ok := seen[inj.ID]; ok {
			return fmt.Errorf("инъекция %q (id=%s): дубль id уже занят инъекцией №%d",
				inj.Name, inj.ID, prev+1)
		}
		seen[inj.ID] = i
	}
	return nil
}

// DecodeInjections разбирает список инъекций из произвольного значения:
// массив объектов, одиночный объект или nil. Источники разные — JSON из REST,
// разобранный в []any, и уже типизированный []map[string]any из аргументов
// инструмента доски, — поэтому значение сначала приводится к JSON, а потом
// разбирается строгим декодером.
//
// Строгость важна: неизвестное поле это ошибка, а не «успех, параметр молча
// потерян» — опечатка в имени поля иначе выглядела бы как применённая
// инъекция с неверным параметром.
func DecodeInjections(raw any) ([]Injection, error) {
	if raw == nil {
		return nil, nil
	}
	if list, ok := raw.([]Injection); ok {
		return list, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	// Форма определяется по первому значимому байту: '[' — массив, иначе
	// одиночный объект.
	for _, b := range data {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			var list []Injection
			if err := decodeStrict(data, &list); err != nil {
				return nil, err
			}
			return list, nil
		default:
			var one Injection
			if err := decodeStrict(data, &one); err != nil {
				return nil, err
			}
			return []Injection{one}, nil
		}
	}
	return nil, nil // пустой ввод
}

// decodeStrict декодирует JSON с запретом неизвестных полей.
func decodeStrict(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}
