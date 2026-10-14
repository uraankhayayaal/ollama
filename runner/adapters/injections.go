package adapters

// Сборка аппликатора промпт-инъекций для агентского цикла (порт
// usecases.InjectionApplier). Источники инъекций и контекст условий собираются
// здесь; сами правила применения живут в application (usecases) и в пакете
// injections.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"ai/agents"
	"ai/application/usecases"
	"ai/board"
	"ai/domain"
	"ai/injections"
	"ai/logging"
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

// newInjectionSet собирает источники инъекций для цикла: инъекции ассистента
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
func injectionMergeContext(ctx context.Context, agent agents.Agent, provider usecases.ChatProvider) injections.MergeContext {
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
		mctx.Role = AgentRole(agent)
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

// AgentRole — роль агента для условий when: приоритет у интерфейса
// GetInjectionRole, затем имя типа агента (Assistant → assistant,
// Developer → developer).
func AgentRole(agent agents.Agent) string {
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

func providerModelName(p usecases.ChatProvider) string {
	if n, ok := p.(modelNamer); ok {
		return n.ModelName()
	}
	return ""
}

func providerName(p usecases.ChatProvider) string {
	if n, ok := p.(providerNamer); ok {
		return n.ProviderName()
	}
	return ""
}

// injectionApplier реализует usecases.InjectionApplier: применяет инъекции к
// истории раунда. Источники (injs) и контекст условий (ctx) фиксируются один
// раз на запуск, чтобы в одном промпте не было двух разных снимков инъекций.
type injectionApplier struct {
	injs injectionSet
	ctx  injections.MergeContext
}

// NewInjectionApplier собирает аппликатор инъекций для запуска агента.
func NewInjectionApplier(ctx context.Context, agent agents.Agent, provider usecases.ChatProvider) usecases.InjectionApplier {
	return injectionApplier{
		injs: newInjectionSet(ctx, agent),
		ctx:  injectionMergeContext(ctx, agent, provider),
	}
}

// Apply применяет инъекции к истории диалога раунда и логирует метаданные.
// Без инъекций возвращает историю как есть (fast-path).
func (a injectionApplier) Apply(history []domain.Message, turn int) []domain.Message {
	all := a.injs.collect()
	if len(all) == 0 {
		return history
	}
	ctxFor := a.ctx
	ctxFor.Turn = turn + 1

	var baseSystem string
	var baseMsgs []injections.Message
	for _, m := range history {
		if m.Role == "system" {
			baseSystem = m.Content
		} else {
			baseMsgs = append(baseMsgs, injections.Message{Role: m.Role, Content: m.Content})
		}
	}
	ctxFor.System = baseSystem
	ctxFor.Messages = baseMsgs

	result, err := injections.ApplyInjections(baseSystem, baseMsgs, all, ctxFor)
	if err != nil || result == nil {
		if err != nil {
			logging.Debugf("RUNNER: инъекции не применены: %v", err)
		}
		return history
	}
	if applied := injections.DescribeApplied(result.Applied); applied != "" {
		logging.Debugf("RUNNER: раунд %d: применены инъекции: %s", turn+1, applied)
	}
	if skipped := injections.DescribeSkipped(result.Skipped); skipped != "" {
		logging.Debugf("RUNNER: раунд %d: инъекции пропущены: %s", turn+1, skipped)
	}
	if result.ReplaceWarning != "" {
		logging.Debugf("RUNNER: раунд %d: %s", turn+1, result.ReplaceWarning)
	}

	out := make([]domain.Message, 0, len(result.Messages)+1)
	if result.System != "" {
		out = append(out, domain.Message{Role: "system", Content: result.System})
	}
	for _, m := range result.Messages {
		out = append(out, domain.Message{Role: m.Role, Content: m.Content})
	}
	return out
}
