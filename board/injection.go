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
