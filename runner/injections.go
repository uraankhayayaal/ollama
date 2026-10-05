package runner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"ai/agents"
	"ai/board"
	"ai/injections"
)

// injectionSet — инъекции агентского цикла, собираемые из источников.
// Список пересобирается перед каждым запросом к модели: живой источник из
// контекста (доска задачи) перечитывается, поэтому правка инъекций видна
// модели без перезапуска агента.
type injectionSet struct {
	assistant []board.Injection
	ctx       context.Context
}

// collect возвращает все инъекции цикла в порядке scope:
// global → assistant → session → runtime(task).
func (s injectionSet) collect() []board.Injection {
	var taskInjs []board.Injection
	if s.ctx != nil {
		taskInjs = board.AllInjections(s.ctx)
	}
	// global-слот оставлен пустым: глобальные инъекции в конфиге приложения
	// не реализованы (план 3.3/6.2). Подключение — одна строка CollectInjections,
	// когда появится источник.
	return injections.CollectInjections(nil, s.assistant, nil, taskInjs)
}

// injectionSet собирает источники инъекций для цикла: инъекции ассистента
// (Assistant.Injections через GetInjections) + инъекции задачи/сессии из
// контекста.
func newInjectionSet(ctx context.Context, agent agents.Agent) injectionSet {
	var assistantInjs []board.Injection
	if p, ok := agent.(interface{ GetInjections() []board.Injection }); ok {
		assistantInjs = p.GetInjections()
	}
	return injectionSet{assistant: assistantInjs, ctx: ctx}
}

// injectionMergeContext наполняет контекст условий when и шаблонов инъекций
// реальными значениями прогона. Раньше здесь стояли только System/Messages/
// Role, из-за чего условия вида `when: "project == \"mytrip\""` или
// `has_files == "true"` никогда не срабатывали — переменные были пустыми.
func injectionMergeContext(ctx context.Context, agent agents.Agent, provider ChatProvider) injections.MergeContext {
	scope := board.InjectionScopeFromContext(ctx)

	mctx := injections.MergeContext{
		Model:    providerModelName(provider),
		Provider: providerName(provider),
		Project:  scope.Project,
		TaskID:   scope.TaskID,
		Role:     scope.Role,
		User:     currentUser(),
		RenderFn: injections.RenderFnDefault,
		EvalFn:   injections.EvalFnDefault,
		Session: map[string]any{
			"task_id": scope.TaskID,
			"project": scope.Project,
			"session": scope.Session,
		},
		Assistant: map[string]any{
			"project": scope.Project,
			"role":    scope.Role,
		},
	}
	if mctx.Role == "" {
		mctx.Role = agentRole(agent)
	}

	// Список инструментов агента — для when-переменной tools.
	if agent != nil {
		names := make([]string, 0, 8)
		for _, td := range agent.GetTools() {
			if td.Name != "" {
				names = append(names, td.Name)
			}
		}
		mctx.Tools = names
		// has_files: у агента есть инструменты работы с файлами проекта.
		hasFiles := false
		for _, n := range names {
			if n == "ReadFiles" || n == "WriteFiles" || n == "CodeSearch" {
				hasFiles = true
				break
			}
		}
		mctx.HasFiles = hasFiles
	}
	return mctx
}

// agentRole — роль агента для условий when: приоритет у интерфейса
// GetInjectionRole, затем имя типа агента (Assistant → assistant,
// Developer → developer).
func agentRole(agent agents.Agent) string {
	if agent == nil {
		return ""
	}
	if r, ok := agent.(interface{ GetInjectionRole() string }); ok {
		if role := r.GetInjectionRole(); role != "" {
			return role
		}
	}
	name := fmt.Sprintf("%T", agent)
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(name)
}

// currentUser — пользователь системы для шаблонов/условий. В режиме одного
// пользователя ( вся система работает от его имени) это USER/LOGNAME.
func currentUser() string {
	if u := os.Getenv("AI_USER"); u != "" {
		return u
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return os.Getenv("LOGNAME")
}

// providerModelName / providerName достают имя модели и провайдера, если
// провайдер их знает (интерфейсы ниже). Без них переменные model/provider в
// условиях when остаются пустыми.
type modelNamer interface{ ModelName() string }
type providerNamer interface{ ProviderName() string }

func providerModelName(p ChatProvider) string {
	if n, ok := p.(modelNamer); ok {
		return n.ModelName()
	}
	return ""
}

func providerName(p ChatProvider) string {
	if n, ok := p.(providerNamer); ok {
		return n.ProviderName()
	}
	return ""
}
