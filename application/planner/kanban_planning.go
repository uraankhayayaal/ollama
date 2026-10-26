package planner

import (
	"ai/domain"
	"ai/shared/tokens"
	"context"
	"fmt"
	"strings"
)

// phaseArchitect: если эпиков на доске ещё нет — запускает Системного
// архитектора. Эпики публикуются его вызовом submit_architecture_backlog.
func (k *KanbanRunner) phaseArchitect(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	if len(epics) > 0 {
		return false, nil
	}

	meta, err := k.store.GetMeta(ctx)
	if err != nil {
		return false, err
	}

	arch := agentFactory().Architect(k.store.Project(), meta.Task, k.store)
	arch = k.prepareArchitect(arch)
	// 4.1: резолвер worktree ветки эпика. Вызывается архитектором ВНУТРИ
	// submitBacklog — после публикации бэклога, когда эпики уже на доске и их
	// ветки созданы хуком EpicCreatedHook. Основной (первый по порядку) эпик
	// получает структуру проекта; без worktree (консоль/не-git) архитектор
	// получит note и структуру не напишет.
	arch = agentFactory().SetArchitectEpicWorkdir(arch, func() (string, string) {
		if k.epicOutputDir == nil {
			return "", ""
		}
		eps, err := k.store.ListEpics(ctx)
		if err != nil || len(eps) == 0 {
			return "", ""
		}
		primary := eps[0]
		for _, e := range eps[1:] {
			if domain.EpicLess(e, primary) {
				primary = e
			}
		}
		dir := k.epicOutputDir(k.store.Project(), primary.TaskID)
		if dir == "" {
			return "", ""
		}
		return dir, primary.GitBranch
	})
	resp, err := k.generate(ctx, tokens.ScopeArchitecture, arch)
	if err != nil {
		return false, k.architectFail(ctx, fmt.Errorf("фаза архитектора: %w", err))
	}
	if err := resp.LoopError("фаза архитектора"); err != nil {
		return false, k.architectFail(ctx, err)
	}
	if resp != nil && resp.Truncated {
		return false, k.architectFail(ctx, fmt.Errorf("фаза архитектора: цикл остановлен по лимиту раундов"))
	}

	epics, err = k.store.ListEpics(ctx)
	if err != nil {
		return false, k.architectFail(ctx, err)
	}
	if len(epics) == 0 {
		return false, k.architectFail(ctx, fmt.Errorf("фаза архитектора: модель не опубликовала эпики (submit_architecture_backlog не вызван)"))
	}
	k.log.Infof("[Системный архитектор] опубликованы эпики: %s", epicList(epics))
	return true, nil
}

// phaseArchitectReview — Ф-8: ревизия эпиков-черновиков (requires_review=true)
// Системным архитектором в режиме ревизора. Черновики, созданные чатом/вручную,
// декомпозиции лидами не подлежат (nextLeadEpic их пропускает), поэтому именно
// архитектор приводит их к стандарту бэклога и снимает флаг — только после этого
// лид получает эпик. Если на доске черновиков нет — фаза простаивает.
// Работает в обоих режимах (обычном и board-only): в board-only чат уже мог
// наложить на доску эпики, требующие ревизии.
func (k *KanbanRunner) phaseArchitectReview(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	var drafts []*domain.Epic
	for _, epic := range epics {
		// Черновики не трогаем только в терминальных/остановленных
		// состояниях («помощь человека» замораживает ревизию, Ф-8): в
		// остальных состояниях ревизия обязательна, пока флаг не снят.
		if !epic.RequiresReview || epic.Status.Terminal() {
			continue
		}
		drafts = append(drafts, epic)
	}
	if len(drafts) == 0 {
		return false, nil
	}

	reviewer := agentFactory().ArchitectReviewer(k.store.Project(), k.epicReviewPrompt(k.store.Project(), drafts), k.store)
	reviewer = k.prepareArchitect(reviewer)
	resp, err := k.generate(ctx, tokens.ScopeArchitecture, reviewer)
	if err != nil {
		return false, fmt.Errorf("фаза ревизии эпиков: %w", err)
	}
	if err := resp.LoopError("фаза ревизии эпиков"); err != nil {
		return false, err
	}
	if resp != nil && resp.Truncated {
		return false, fmt.Errorf("фаза ревизии эпиков: цикл остановлен по лимиту раундов")
	}
	k.log.Infof("[Системный архитектор] ревизия %d эпиков-черновиков", len(drafts))

	// Ревизия успешна: снимаем флаг и синхронизируем ревизию лида с ревизией
	// эпика, чтобы лид сразу принял утверждённый эпик к декомпозиции без
	// повторной «ресинхронизации» (LeadSyncedRev == Revision).
	for _, d := range drafts {
		d.RequiresReview = false
		d.LeadSyncedRev = d.Revision
		if err := k.store.SaveEpic(ctx, d); err != nil {
			return false, fmt.Errorf("эпик %s: снятие ревизии: %w", d.TaskID, err)
		}
	}
	return true, nil
}

// epicReviewPrompt формирует задание ревизии эпиков-черновиков (Ф-8): архитектор
// приводит каждый черновик к стандарту бэклога (ТЗ, стек, роли, глубина
// декомпозиции, смежные модули).
func (k *KanbanRunner) epicReviewPrompt(project string, drafts []*domain.Epic) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Проведи ревизию %d эпиков-черновиков на доске проекта %q. Это черновики, созданные чат-ассистентом или вручную: каждому требуется твоя экспертиза перед декомпозицией.\n\n", len(drafts), project)
	for _, epic := range drafts {
		fmt.Fprintf(&b, "- %s «%s» (статус %s, ревизия %d): %s\n",
			epic.TaskID, truncateText(epic.Title, 80), epic.Status.Label(), epic.Revision, truncateText(epic.Description, 400))
	}
	b.WriteString("\nДля каждого эпика: проверь корректность ТЗ, соответствие фактическому стеку проекта (DetectStack), глубину декомпозиции и затронутые смежные модули; скорректируй эпик инструментами доски (BoardUpdateEpic и др.).")
	return b.String()
}
