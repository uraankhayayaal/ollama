// Учёт расхода LLM-токенов по задачам и эпикам (Ф-2/Ф-3
// PLAN-2026-09-19-done-epic-task-token.md).
//
// Схема работы:
//
//  1. Каждый вызов провайдера оборачивается scope'ом единицы работы
//     (k.generate): специалист — task:<id>, лид — epic:<id>, архитектор —
//     architecture, QA/экспертиза — bugs. Раунды копятся в Redis-счётчике
//     scope'а (tokens.Store.AddScoped) без involvement доски.
//  2. Когда единица доходит до терминального статуса, оркестратор снимает
//     счётчик и записывает факт в сущность доски (board.Finalize*Tokens),
//     после чего обнуляет счётчик — двойного счёта нет.
//  3. Итог эпика = сумма фактов его задач + вклад лида (scope epic:<id>) +
//     доля архитектора. Доля архитектора распределяется только когда эпик на
//     доске единственный: при нескольких эпиках «кому именно» относить
//     расход архитектора — догадка, поэтому такой расход остаётся в счётчике
//     проекта (см. epicTokens).
package planner

import (
	"context"

	"ai/agents"
	"ai/board"
	"ai/runevents"
	"ai/runner"
	"ai/tokens"
)

// generate вызывает провайдера в scope'е единицы работы: расход токенов раундов
// атрибутируется этой единице и по нему записывается факт при завершении
// (Ф-2). Пустой scope — расход без атрибуции (счётчик проекта).
func (k *KanbanRunner) generate(ctx context.Context, scope string, a agents.Agent) (*runner.AgentResponse, error) {
	return k.provider.Generate(runevents.WithScope(ctx, scope), a)
}

// finalizeTaskTokens фиксирует факт расхода токенов терминальной задачи:
// снимает накопленное по её scope, записывает в доску и обнуляет счётчик.
// Идемпотентна: повторный вызов ничего не меняет (суммы уже в доске, счётчик
// пуст). Без настроенного счётчика (консоль) — no-op.
func (k *KanbanRunner) finalizeTaskTokens(ctx context.Context, taskID string) error {
	if k.tokens == nil {
		return nil
	}
	scope := tokens.ScopeTask(taskID)
	in, out, err := k.tokens.GetScoped(ctx, scope)
	if err != nil {
		return err
	}
	if in == 0 && out == 0 {
		return nil
	}
	if err := k.store.FinalizeTaskTokens(ctx, taskID, in, out); err != nil {
		return err
	}
	if err := k.tokens.ResetScoped(ctx, scope); err != nil {
		return err
	}
	k.log.Infof("[учёт токенов] задача %s: факт %d токенов (вход %d, выход %d)", taskID, in+out, in, out)
	return nil
}

// finalizeEpicTokens фиксирует факт расхода токенов терминального эпика.
// Итог = сумма фактов его задач (уже записанных на доске) + расход лидов и
// архитектора. Расход лида берётся из scope'а эпика; расход архитектора
// добавляется только когда эпик на доске единственный (иначе остаётся в
// счётчике проекта — см. комментарий к пакету). Идемпотентна.
func (k *KanbanRunner) finalizeEpicTokens(ctx context.Context, epicID string) error {
	if k.tokens == nil {
		return nil
	}
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return err
	}
	tasks, err := k.store.TasksByEpic(ctx, epicID)
	if err != nil {
		return err
	}
	in, out := epicTokens(tasks)

	leadIn, leadOut, err := k.tokens.GetScoped(ctx, tokens.ScopeEpic(epicID))
	if err != nil {
		return err
	}
	in += leadIn
	out += leadOut

	// Архитектура — общий расход проекта: относим к эпику только если он один.
	archIn, archOut := int64(0), int64(0)
	if len(epics) == 1 {
		if archIn, archOut, err = k.tokens.GetScoped(ctx, tokens.ScopeArchitecture); err != nil {
			return err
		}
		in += archIn
		out += archOut
	}

	// Факт эпика записывается один раз: счётчики после этого обнуляются, и
	// повторный проход (sweep) посчитал бы только сумму задач — уже без вклада
	// лида и архитектора. От «уменьшения итога» страхует board.FinalizeEpicTokens.
	if err := k.store.FinalizeEpicTokens(ctx, epicID, in, out); err != nil {
		return err
	}
	// Счётчики лида/архитектора обнуляем: факт уже на доске.
	if err := k.tokens.ResetScoped(ctx, tokens.ScopeEpic(epicID)); err != nil {
		return err
	}
	if archIn > 0 || archOut > 0 {
		if err := k.tokens.ResetScoped(ctx, tokens.ScopeArchitecture); err != nil {
			return err
		}
	}
	k.log.Infof("[учёт токенов] эпик %s: факт %d токенов (вход %d, выход %d)", epicID, in+out, in, out)
	return nil
}

// epicTokens суммирует факт расхода токенов по задачам эпика.
func epicTokens(tasks []*board.Task) (in, out int64) {
	for _, t := range tasks {
		in += t.TokensInput
		out += t.TokensOutput
	}
	return in, out
}

// finalizeTokens проходит по доске и фиксирует факт по всем терминальным
// единицам, у которых ещё не записан итог. Вызывается раз в цикл
// (runPhases) — закрывает переходы в done/cancelled, сделанные не только
// оркестратором: специалистом, чат-ассистентом или человеком в Web UI.
// Идемпотентна: записи с уже записанным итогом и пустыми счётчиками
// пропускаются без записи в Redis.
func (k *KanbanRunner) finalizeTokens(ctx context.Context) {
	if k.tokens == nil {
		return
	}
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		k.log.Warnf("[учёт токенов] чтение задач: %v", err)
		return
	}
	for _, t := range tasks {
		if !t.Status.Terminal() {
			continue
		}
		if err := k.finalizeTaskTokens(ctx, t.TaskID); err != nil {
			k.log.Warnf("[учёт токенов] задача %s: %v", t.TaskID, err)
		}
	}
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		k.log.Warnf("[учёт токенов] чтение эпиков: %v", err)
		return
	}
	for _, e := range epics {
		if !e.Status.Terminal() {
			continue
		}
		if err := k.finalizeEpicTokens(ctx, e.TaskID); err != nil {
			k.log.Warnf("[учёт токенов] эпик %s: %v", e.TaskID, err)
		}
	}
}
