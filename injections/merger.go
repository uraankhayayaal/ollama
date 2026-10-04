// Package injections — пайплайн применения промпт-инъекций к работающей
// модели. Собирает инъекции из всех источников (global → assistant → session
// → runtime), фильтрует по условиям when, сортирует по scope и priority,
// применяет к system/messages с учётом target и position.
package injections

import (
	"fmt"
	"sort"
	"strings"

	"ai/board"
)

// MergeContext — контекст для применения инъекций.
type MergeContext struct {
	System    string
	Messages  []Message
	Model     string
	Provider  string
	Role      string
	Project   string
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
type AppliedInjection struct {
	ID       string
	Name     string
	Scope    string
	Target   string
	Position string
}

// MergeResult — результат применения инъекций.
type MergeResult struct {
	System         string
	Messages       []Message
	Applied        []AppliedInjection
	ReplaceWarning string
}

// MaxContentSize — максимальный размер контента одной инъекции (байт).
const MaxContentSize = 10 * 1024

// MaxTotalSize — максимальный суммарный размер всех инъекций (байт).
const MaxTotalSize = 50 * 1024

// ApplyInjections применяет инъекции к базовому system и messages.
// Порядок: global → assistant → session → runtime.
// Внутри scope: по priority (убывание), при равенстве — порядок объявления.
func ApplyInjections(baseSystem string, baseMessages []Message, injections []board.Injection, ctx MergeContext) (*MergeResult, error) {
	if len(injections) == 0 {
		return &MergeResult{
			System:   baseSystem,
			Messages: baseMessages,
		}, nil
	}

	filtered := filterInjections(injections, ctx)
	if len(filtered) == 0 {
		return &MergeResult{
			System:   baseSystem,
			Messages: baseMessages,
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
		content := inj.Content
		if ctx.RenderFn != nil {
			content = ctx.RenderFn(content, ctx)
		}

		if len(content) > MaxContentSize {
			content = content[:MaxContentSize] + "\n... [truncated]"
		}
		totalSize += len(content)
		if totalSize > MaxTotalSize {
			replaceWarning = fmt.Sprintf("превышен лимит суммарного размера инъекций (%d байт), остальные пропущены", MaxTotalSize)
			break
		}

		switch inj.Target {
		case "system":
			system = applyToSystem(system, content, inj)
		case "messages", "user_last", "assistant_last":
			messages = applyToMessages(messages, content, inj)
		case "before_tools", "after_tools":
		}

		applied = append(applied, AppliedInjection{
			ID:       inj.ID,
			Name:     inj.Name,
			Scope:    inj.Scope,
			Target:   inj.Target,
			Position: inj.Position,
		})
	}

	return &MergeResult{
		System:         system,
		Messages:       messages,
		Applied:        applied,
		ReplaceWarning: replaceWarning,
	}, nil
}

func filterInjections(injs []board.Injection, ctx MergeContext) []board.Injection {
	out := make([]board.Injection, 0, len(injs))
	for _, inj := range injs {
		if !inj.IsEnabled() {
			continue
		}
		if inj.When != "" && ctx.EvalFn != nil {
			if !ctx.EvalFn(inj.When, ctx) {
				continue
			}
		}
		out = append(out, inj)
	}
	return out
}

func sortInjections(injs []board.Injection) {
	sort.SliceStable(injs, func(i, j int) bool {
		si := scopePriority(injs[i].Scope)
		sj := scopePriority(injs[j].Scope)
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
	case "global":
		return 0
	case "assistant":
		return 1
	case "session":
		return 2
	case "runtime":
		return 3
	default:
		return 4
	}
}

func applyToSystem(system, content string, inj board.Injection) string {
	switch inj.Position {
	case "prepend":
		return content + "\n\n" + system
	case "append":
		return system + "\n\n" + content
	case "replace":
		return content
	case "inject_at_index":
		if inj.Index == 0 {
			return content + "\n\n" + system
		}
		return system + "\n\n" + content
	default:
		return system + "\n\n" + content
	}
}

func applyToMessages(messages []Message, content string, inj board.Injection) []Message {
	switch inj.Target {
	case "user_last":
		return applyToLastMessage(messages, content, inj, "user")
	case "assistant_last":
		return applyToLastMessage(messages, content, inj, "assistant")
	case "messages":
		return applyToMessagesGeneric(messages, content, inj)
	default:
		return messages
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
	case "prepend":
		messages[idx].Content = content + "\n\n" + messages[idx].Content
	case "append":
		messages[idx].Content = messages[idx].Content + "\n\n" + content
	case "replace":
		messages[idx].Content = content
	default:
		messages[idx].Content = messages[idx].Content + "\n\n" + content
	}
	return messages
}

func applyToMessagesGeneric(messages []Message, content string, inj board.Injection) []Message {
	switch inj.Position {
	case "prepend":
		newMsg := Message{Role: "system", Content: content}
		return append([]Message{newMsg}, messages...)
	case "append":
		newMsg := Message{Role: "system", Content: content}
		return append(messages, newMsg)
	case "replace":
		return []Message{{Role: "system", Content: content}}
	case "inject_at_index":
		idx := inj.Index
		if idx > len(messages) {
			idx = len(messages)
		}
		newMsg := Message{Role: "system", Content: content}
		out := make([]Message, 0, len(messages)+1)
		out = append(out, messages[:idx]...)
		out = append(out, newMsg)
		out = append(out, messages[idx:]...)
		return out
	default:
		newMsg := Message{Role: "system", Content: content}
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
	return []string{"global", "assistant", "session", "runtime"}
}

// ValidateMergeContext проверяет контекст перед применением.
func ValidateMergeContext(ctx MergeContext) error {
	if strings.TrimSpace(ctx.System) == "" && len(ctx.Messages) == 0 {
		return fmt.Errorf("injections: пустой system и messages")
	}
	return nil
}
