package board

import (
	"context"

	"ai/domain"
)

// Промпт-инъекции: структурированные фрагменты, которые применяются к
// работающей модели в момент формирования запроса (runner). Инъекция живёт
// на конкретной задаче доски (Task.Injections) либо в сессии чата
// (Session.sessionInjections) и применяется к СЛЕДУЮЩЕМУ обращению к модели —
// в том числе к следующему раунду уже идущего агентского цикла.
//
// Типы и канонические значения принадлежат домену (domain.Injection); здесь —
// алиасы.

const (
	InjectionScopeGlobal    = domain.InjectionScopeGlobal
	InjectionScopeAssistant = domain.InjectionScopeAssistant
	InjectionScopeSession   = domain.InjectionScopeSession
	InjectionScopeRuntime   = domain.InjectionScopeRuntime
	InjectionScopeTask      = domain.InjectionScopeTask
)

const (
	InjectionTargetSystem        = domain.InjectionTargetSystem
	InjectionTargetMessages      = domain.InjectionTargetMessages
	InjectionTargetUserLast      = domain.InjectionTargetUserLast
	InjectionTargetAssistantLast = domain.InjectionTargetAssistantLast
)

const (
	InjectionPosPrepend       = domain.InjectionPosPrepend
	InjectionPosAppend        = domain.InjectionPosAppend
	InjectionPosReplace       = domain.InjectionPosReplace
	InjectionPosInjectAtIndex = domain.InjectionPosInjectAtIndex
)

// DefaultInjectionPosition — позиция по умолчанию: дописать в конец блока.
const DefaultInjectionPosition = domain.DefaultInjectionPosition

// MaxInjectionContentSize — потолок тела одной инъекции (байт).
const MaxInjectionContentSize = domain.MaxInjectionContentSize

// Injection — промпт-инъекция, привязанная к задаче (Task.Injections) или к
// сессии чата. Тип принадлежит домену (domain.Injection), здесь — алиас.
type Injection = domain.Injection

// NewInjectionID — новый идентификатор инъекции (16 байт энтропии в hex).
func NewInjectionID() string { return domain.NewInjectionID() }

// ValidateInjections нормализует и проверяет список инъекций.
func ValidateInjections(injs []Injection) error { return domain.ValidateInjections(injs) }

// DecodeInjections разбирает список инъекций из произвольного значения
// (массив объектов, одиночный объект или nil) строгим декодером.
func DecodeInjections(raw any) ([]Injection, error) { return domain.DecodeInjections(raw) }

// ---- Контекст инъекций ----
// Инъекции попадают в агентский цикл через контекст: снапшот (NewInjectionContext)
// и/или живой источник (WithInjectionSource), который перечитывает доску перед
// каждым запросом к модели. Живой источник и есть «горячее применение»: правка
// задачи видна следующему обращению к модели, не дожидаясь нового прогона.
//
// Реализация принадлежит домену (domain); здесь — обёртки/алиасы для обратной
// совместимости.

// InjectionSource — живой поставщик инъекций (domain.InjectionSource).
type InjectionSource = domain.InjectionSource

// InjectionScope — контекстная рамка агентского цикла (domain.InjectionScope).
type InjectionScope = domain.InjectionScope

// NewInjectionContext кладёт снапшот инъекций в контекст агента.
func NewInjectionContext(ctx context.Context, injs []Injection) context.Context {
	return domain.NewInjectionContext(ctx, injs)
}

// InjectionsFromContext достаёт снапшот инъекций из контекста.
func InjectionsFromContext(ctx context.Context) []Injection {
	return domain.InjectionsFromContext(ctx)
}

// WithInjectionSource кладёт живой поставщик инъекций в контекст.
func WithInjectionSource(ctx context.Context, src InjectionSource) context.Context {
	return domain.WithInjectionSource(ctx, src)
}

// InjectionSourceFromContext достаёт живого поставщика инъекций.
func InjectionSourceFromContext(ctx context.Context) InjectionSource {
	return domain.InjectionSourceFromContext(ctx)
}

// WithInjectionScope кладёт рамку прогона в контекст.
func WithInjectionScope(ctx context.Context, sc InjectionScope) context.Context {
	return domain.WithInjectionScope(ctx, sc)
}

// InjectionScopeFromContext достаёт рамку прогона.
func InjectionScopeFromContext(ctx context.Context) InjectionScope {
	return domain.InjectionScopeFromContext(ctx)
}

// AllInjections отдаёт инъекции для текущего запроса к модели.
func AllInjections(ctx context.Context) []Injection {
	return domain.AllInjections(ctx)
}
