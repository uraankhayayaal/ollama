package board

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Промпт-инъекции: структурированные фрагменты, которые применяются к
// работающей модели в момент формирования запроса (runner). Инъекция живёт
// на конкретной задаче доски (Task.Injections) либо в сессии чата
// (Session.sessionInjections) и применяется к СЛЕДУЮЩЕМУ обращению к модели —
// в том числе к следующему раунду уже идущего агентского цикла.

const (
	// InjectionScopeGlobal — инъекции уровня приложения (резерв: глобальный
	// конфиг). Приоритет самый низкий.
	InjectionScopeGlobal = "global"
	// InjectionScopeAssistant — инъекции конкретного ассистента.
	InjectionScopeAssistant = "assistant"
	// InjectionScopeSession — инъекции сессии чата.
	InjectionScopeSession = "session"
	// InjectionScopeRuntime — инъекции текущего прогона агента (задачи).
	InjectionScopeRuntime = "runtime"
	// InjectionScopeTask — синоним runtime в записях задачи: инъекция
	// привязана к конкретной задаче и умирает вместе с ней.
	InjectionScopeTask = "task"
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

// DefaultInjectionPosition — позиция по умолчанию: дописать в конец блока.
const DefaultInjectionPosition = InjectionPosAppend

// MaxInjectionContentSize — потолок тела одной инъекции (байт). Проверяется
// на записи (валидация) и на применении (обрезка с маркером).
const MaxInjectionContentSize = 10 * 1024

// Injection — промпт-инъекция, привязанная к задаче (Task.Injections) или к
// сессии чата. Значение Enabled — три состояния: nil = не задано (активна),
// чтобы запись без поля работала как раньше; явный false отключает инъекцию.
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

// IsEnabled — инъекция активна, пока её явно не отключили.
func (inj Injection) IsEnabled() bool { return inj.Enabled == nil || *inj.Enabled }

// SetEnabled включает/выключает инъекцию.
func (inj *Injection) SetEnabled(v bool) { inj.Enabled = &v }

// EffectiveScope — scope с учётом синонима task → runtime: пайплайн сортирует
// инъекции по scope, и «task» должен весить столько же, сколько runtime.
func (inj Injection) EffectiveScope() string {
	if inj.Scope == InjectionScopeTask {
		return InjectionScopeRuntime
	}
	return inj.Scope
}

// Normalize подставляет дефолты записи (scope, позиция) и выдаёт id, если его
// не задали. Вызывается перед сохранением, чтобы в доске лежали уже
// канонические значения.
//
// Именно здесь выдаётся id: без него инъекцию, добавленную через PUT
// /tasks/{tid} или инструмент доски, нельзя было бы адресовать в DELETE
// /tasks/{tid}/injections/{id} — таких записей просто не существовало бы по
// имени.
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

// ---- Контекст инъекций ----
// Инъекции попадают в агентский цикл через контекст: снапшот (NewInjectionContext)
// и/или живой источник (WithInjectionSource), который перечитывает доску перед
// каждым запросом к модели. Живой источник и есть «горячее применение»: правка
// задачи видна следующему обращению к модели, не дожидаясь нового прогона.

type injectionKey struct{}

// NewInjectionContext кладёт снапшот инъекций в контекст агента.
func NewInjectionContext(ctx context.Context, injs []Injection) context.Context {
	if len(injs) == 0 {
		return ctx // fast-path: инъекций нет — контекст не трогаем.
	}
	return context.WithValue(ctx, injectionKey{}, injs)
}

// InjectionsFromContext достаёт снапшот инъекций из контекста.
func InjectionsFromContext(ctx context.Context) []Injection {
	if ctx == nil {
		return nil
	}
	if injs, ok := ctx.Value(injectionKey{}).([]Injection); ok {
		return injs
	}
	return nil
}

// InjectionSource — живой поставщик инъекций. Вызывается перед каждым
// запросом к модели; ошибка внутри подавляется вызывающим (инъекции не
// должны ронять агентский цикл).
//
// Ошибка различает два разных состояния, и это не формальность: «доска
// недоступна» (падает обратно на снапшот) и «пользователь удалил все
// инъекции» (нужно уважать — снапшот воскресил бы удалённое). Поэтому
// пустой список без ошибки означает именно «инъекций нет».
type InjectionSource func(ctx context.Context) ([]Injection, error)

type injectionSourceKey struct{}

// WithInjectionSource кладёт живой поставщик инъекций в контекст.
func WithInjectionSource(ctx context.Context, src InjectionSource) context.Context {
	if src == nil {
		return ctx
	}
	return context.WithValue(ctx, injectionSourceKey{}, src)
}

// InjectionSourceFromContext достаёт живого поставщика инъекций.
func InjectionSourceFromContext(ctx context.Context) InjectionSource {
	if ctx == nil {
		return nil
	}
	src, _ := ctx.Value(injectionSourceKey{}).(InjectionSource)
	return src
}

// InjectionScope — контекстная рамка агентского цикла: что за проект, какая
// задача и кто исполнитель. Попадает в MergeContext (переменные when/шаблонов)
// и в диагностику применённых инъекций.
type InjectionScope struct {
	Project string // имя проекта
	TaskID  string // задача доски, для которой идёт прогон
	Role    string // исполнитель: backend, frontend, architect, assistant…
	Session string // идентификатор сессии чата (для чат-ассистента)
}

type injectionScopeKey struct{}

// WithInjectionScope кладёт рамку прогона в контекст.
func WithInjectionScope(ctx context.Context, sc InjectionScope) context.Context {
	if sc.Project == "" && sc.TaskID == "" && sc.Role == "" && sc.Session == "" {
		return ctx
	}
	return context.WithValue(ctx, injectionScopeKey{}, sc)
}

// InjectionScopeFromContext достаёт рамку прогона.
func InjectionScopeFromContext(ctx context.Context) InjectionScope {
	if ctx == nil {
		return InjectionScope{}
	}
	sc, _ := ctx.Value(injectionScopeKey{}).(InjectionScope)
	return sc
}

// AllInjections отдаёт инъекции для текущего запроса к модели.
//
// Живой источник, если он есть, АВТОРИТЕТЕН и полностью заменяет снапшот:
// оба читают одну и ту же запись (у задачи — доску, у сессии — память
// процесса), поэтому «живой + снапшот» означал бы ровно одно — воскрешение
// удалённого. Пользователь снёс инъекцию посреди прогона, а она продолжала
// уходить в модель из стартового снимка до конца цикла.
//
// Снапшот — только запасной вариант: источника нет вовсе или он не смог
// прочитать запись (доска недоступна). Тогда лучше применить прежний текст,
// чем молча выключить инструкцию, — цикл не должен зависеть от живости
// доски.
func AllInjections(ctx context.Context) []Injection {
	src := InjectionSourceFromContext(ctx)
	if src != nil {
		live, err := src(ctx)
		if err == nil {
			return live
		}
	}
	return InjectionsFromContext(ctx)
}
