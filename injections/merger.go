// Package injections — пайплайн применения промпт-инъекций к работающей
// модели. Собирает инъекции из всех источников (global → assistant → session
// → runtime), фильтрует по условиям when, сортирует по scope и priority,
// применяет к system/messages с учётом target и position.
//
// Инъекции применяются к СЛЕДУЮЩЕМУ запросу к модели: runner вызывает
// ApplyInjections перед каждым обращением, поэтому правка доски видна модели
// без перезапуска агентского цикла.
package injections

import (
	"fmt"
	"sort"
	"strings"

	"ai/board"
)

// MergeContext — контекст для применения инъекций: что сейчас в промпте и
// параметры прогона (для условий when и шаблонов).
type MergeContext struct {
	System    string
	Messages  []Message
	Model     string
	Provider  string
	Role      string
	Project   string
	TaskID    string
	Turn      int
	User      string
	Tools     []string
	HasFiles  bool
	Vars      map[string]any
	Env       map[string]string
	Session   map[string]any
	Assistant map[string]any
	// RenderFn — функция рендеринга шаблонов ({{vars.*}}, {{env.*}} и т.д.).
	// Передаётся извне, чтобы избежать циклического импорта.
	RenderFn func(content string, ctx MergeContext) string
	// EvalFn — функция вычисления условий when. Передаётся извне.
	EvalFn func(when string, ctx MergeContext) bool
}

// Message — сообщение в пайплайне инъекций (упрощённая копия runner.Message).
type Message struct {
	Role    string
	Content string
}

// AppliedInjection — метаданные применённой инъекции (для логов/отладки).
// Содержимое инъекции НЕ логируется: в промпте могут быть чувствительные
// строки, в логах им не место.
type AppliedInjection struct {
	ID       string
	Name     string
	Scope    string
	Target   string
	Position string
}

// SkippedInjection — метаданные инъекции, которую пропустили (выключена,
// условие when ложно, не прошла валидацию).
type SkippedInjection struct {
	ID     string
	Name   string
	Reason string
}

// MergeResult — результат применения инъекций.
type MergeResult struct {
	System         string
	Messages       []Message
	Applied        []AppliedInjection
	Skipped        []SkippedInjection
	ReplaceWarning string
}

// MaxContentSize — максимальный размер контента одной инъекции (байт).
const MaxContentSize = board.MaxInjectionContentSize

// MaxTotalSize — максимальный суммарный размер всех инъекций (байт).
const MaxTotalSize = 50 * 1024

// ApplyInjections применяет инъекции к базовому system и messages.
// Порядок: global → assistant → session → runtime (task).
// Внутри scope: по priority (убывание), при равенстве — порядок объявления.
// Инъекции с одинаковым id схлопываются: последняя перекрывает (DeduplicateByID).
func ApplyInjections(baseSystem string, baseMessages []Message, injections []board.Injection, ctx MergeContext) (*MergeResult, error) {
	if len(injections) == 0 {
		return &MergeResult{
			System:   baseSystem,
			Messages: baseMessages,
		}, nil
	}

	// Дедупликация до фильтрации: перекрытая инъекция не должна даже
	// «сработать» в when-условии, иначе её влияние зависит от порядка.
	injections = DeduplicateByID(injections)

	filtered, skipped := filterInjections(injections, ctx)
	if len(filtered) == 0 {
		return &MergeResult{
			System:   baseSystem,
			Messages: baseMessages,
			Skipped:  skipped,
		}, nil
	}

	sortInjections(filtered)

	system := baseSystem
	messages := make([]Message, len(baseMessages))
	copy(messages, baseMessages)

	var applied []AppliedInjection
	var replaceWarning string
	totalSize := 0

	for _, inj := range filtered {
		// Валидация на лету: запись могла прийти из доски, отредактированной
		// вручную, — мусор в промпт не попадает, а попадает в Skipped.
		if err := inj.Validate(); err != nil {
			skipped = append(skipped, SkippedInjection{
				ID:     inj.ID,
				Name:   inj.Name,
				Reason: err.Error(),
			})
			continue
		}

		content := inj.Content
		if ctx.RenderFn != nil {
			content = ctx.RenderFn(content, ctx)
		}
		content = TruncateContent(content, MaxContentSize)
		totalSize += len(content)
		if totalSize > MaxTotalSize {
			replaceWarning = fmt.Sprintf("превышен лимит суммарного размера инъекций (%d байт), остальные пропущены", MaxTotalSize)
			break
		}

		switch inj.Target {
		case board.InjectionTargetSystem:
			system = applyToSystem(system, content, inj)
		case board.InjectionTargetMessages, board.InjectionTargetUserLast, board.InjectionTargetAssistantLast:
			messages = applyToMessages(messages, content, inj)
		default:
			// Неизвестный target отсеян Validate выше; на всякий случай не
			// записываем такую инъекцию в Applied.
			skipped = append(skipped, SkippedInjection{
				ID:     inj.ID,
				Name:   inj.Name,
				Reason: "неизвестный target " + inj.Target,
			})
			continue
		}

		applied = append(applied, AppliedInjection{
			ID:       inj.ID,
			Name:     inj.Name,
			Scope:    inj.EffectiveScope(),
			Target:   inj.Target,
			Position: inj.Position,
		})
	}

	return &MergeResult{
		System:         system,
		Messages:       messages,
		Applied:        applied,
		Skipped:        skipped,
		ReplaceWarning: replaceWarning,
	}, nil
}

// filterInjections отсеивает выключенные инъекции и те, чьё условие when
// ложно. Отсеянные возвращаются вторым значением — для наблюдаемости.
func filterInjections(injs []board.Injection, ctx MergeContext) ([]board.Injection, []SkippedInjection) {
	out := make([]board.Injection, 0, len(injs))
	var skipped []SkippedInjection
	for _, inj := range injs {
		if !inj.IsEnabled() {
			skipped = append(skipped, SkippedInjection{ID: inj.ID, Name: inj.Name, Reason: "выключена (enabled=false)"})
			continue
		}
		if inj.When != "" && ctx.EvalFn != nil {
			if !ctx.EvalFn(inj.When, ctx) {
				skipped = append(skipped, SkippedInjection{ID: inj.ID, Name: inj.Name, Reason: "условие when ложно: " + inj.When})
				continue
			}
		}
		out = append(out, inj)
	}
	return out, skipped
}

func sortInjections(injs []board.Injection) {
	sort.SliceStable(injs, func(i, j int) bool {
		si := scopePriority(injs[i].EffectiveScope())
		sj := scopePriority(injs[j].EffectiveScope())
		if si != sj {
			return si < sj
		}
		if injs[i].Priority != injs[j].Priority {
			return injs[i].Priority > injs[j].Priority
		}
		return false
	})
}

func scopePriority(scope string) int {
	switch scope {
	case board.InjectionScopeGlobal:
		return 0
	case board.InjectionScopeAssistant:
		return 1
	case board.InjectionScopeSession:
		return 2
	case board.InjectionScopeRuntime:
		return 3
	default:
		return 4
	}
}

func applyToSystem(system, content string, inj board.Injection) string {
	switch inj.Position {
	case board.InjectionPosPrepend:
		if strings.TrimSpace(system) == "" {
			return content
		}
		return content + "\n\n" + system
	case board.InjectionPosReplace:
		return content
	case board.InjectionPosInjectAtIndex:
		if inj.Index == 0 {
			return prependToSystem(system, content)
		}
		return appendToSystem(system, content)
	default: // append
		return appendToSystem(system, content)
	}
}

func prependToSystem(system, content string) string {
	if strings.TrimSpace(system) == "" {
		return content
	}
	return content + "\n\n" + system
}

func appendToSystem(system, content string) string {
	if strings.TrimSpace(system) == "" {
		return content
	}
	return system + "\n\n" + content
}

func applyToMessages(messages []Message, content string, inj board.Injection) []Message {
	switch inj.Target {
	case board.InjectionTargetUserLast:
		return applyToLastMessage(messages, content, inj, "user")
	case board.InjectionTargetAssistantLast:
		return applyToLastMessage(messages, content, inj, "assistant")
	default:
		return applyToMessagesGeneric(messages, content, inj)
	}
}

func applyToLastMessage(messages []Message, content string, inj board.Injection, role string) []Message {
	idx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == role {
			idx = i
			break
		}
	}
	if idx < 0 {
		return messages
	}

	switch inj.Position {
	case board.InjectionPosPrepend:
		messages[idx].Content = prependToSystem(messages[idx].Content, content)
	case board.InjectionPosReplace:
		messages[idx].Content = content
	case board.InjectionPosInjectAtIndex:
		messages[idx].Content = appendToSystem(messages[idx].Content, content)
	default: // append
		messages[idx].Content = appendToSystem(messages[idx].Content, content)
	}
	return messages
}

// applyToMessagesGeneric работает с target=messages. Новые сообщения
// получают роль user (обычное продолжение диалога, которое модель ожидает
// увидеть), а не system: роль system допустима лишь в начале истории, и
// системный блок, который приходит на середину, ломает некоторые провайдеры.
func applyToMessagesGeneric(messages []Message, content string, inj board.Injection) []Message {
	newMsg := Message{Role: "user", Content: content}
	switch inj.Position {
	case board.InjectionPosPrepend:
		return append([]Message{newMsg}, messages...)
	case board.InjectionPosReplace:
		// replace у messages оставляет саму инъекцию: история диалога без
		// исходных сообщений бессмысленна.
		return []Message{newMsg}
	case board.InjectionPosInjectAtIndex:
		idx := inj.Index
		if idx > len(messages) {
			idx = len(messages)
		}
		if idx < 0 {
			idx = 0
		}
		out := make([]Message, 0, len(messages)+1)
		out = append(out, messages[:idx]...)
		out = append(out, newMsg)
		out = append(out, messages[idx:]...)
		return out
	default: // append
		return append(messages, newMsg)
	}
}

// CollectInjections собирает инъекции из всех источников в правильном порядке.
func CollectInjections(global, assistant, session, runtime []board.Injection) []board.Injection {
	out := make([]board.Injection, 0, len(global)+len(assistant)+len(session)+len(runtime))
	out = append(out, global...)
	out = append(out, assistant...)
	out = append(out, session...)
	out = append(out, runtime...)
	return out
}

// DeduplicateByID удаляет дубликаты по id (последний перекрывает).
func DeduplicateByID(injs []board.Injection) []board.Injection {
	seen := make(map[string]int, len(injs))
	out := make([]board.Injection, 0, len(injs))
	for _, inj := range injs {
		if inj.ID != "" {
			if idx, ok := seen[inj.ID]; ok {
				out[idx] = inj
				continue
			}
			seen[inj.ID] = len(out)
		}
		out = append(out, inj)
	}
	return out
}

// TruncateContent обрезает контент до лимита с маркером.
func TruncateContent(content string, max int) string {
	if len(content) <= max {
		return content
	}
	return content[:max] + "\n... [truncated]"
}

// ScopeOrder возвращает порядок scope для отладки.
func ScopeOrder() []string {
	return []string{
		board.InjectionScopeGlobal,
		board.InjectionScopeAssistant,
		board.InjectionScopeSession,
		board.InjectionScopeRuntime,
	}
}

// DescribeApplied собирает компактную строку метаданных применённых инъекций
// для debug-лога: «id/name[scope/target/position]». Содержимое не попадает.
func DescribeApplied(applied []AppliedInjection) string {
	if len(applied) == 0 {
		return ""
	}
	parts := make([]string, 0, len(applied))
	for _, a := range applied {
		id := a.ID
		if id == "" {
			id = "-"
		}
		parts = append(parts, fmt.Sprintf("%s/%s[%s→%s/%s]", id, a.Name, a.Scope, a.Target, a.Position))
	}
	return strings.Join(parts, " ")
}

// DescribeSkipped собирает строку метаданных пропущенных инъекций.
func DescribeSkipped(skipped []SkippedInjection) string {
	if len(skipped) == 0 {
		return ""
	}
	parts := make([]string, 0, len(skipped))
	for _, s := range skipped {
		parts = append(parts, fmt.Sprintf("%s: %s", s.Name, s.Reason))
	}
	return strings.Join(parts, "; ")
}
