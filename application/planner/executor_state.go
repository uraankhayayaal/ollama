package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ai/application/usecases"
	"ai/domain"
)

// persistPlan обновляет PlanJSON в чекпоинте, чтобы декомпозиции лидов
// (Step.Tasks с контрактами) переживали resume. Без чекпоинта — no-op.
// Сериализуется e.mu: параллельные шаги волны не должны терять обновления
// PLAN.json (Store — read-modify-write).
func (e *Executor) persistPlan(ctx context.Context) {
	if e.store == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	planJSON, err := json.Marshal(e.plan)
	if err != nil {
		e.log.Warnf("[Checkpoint] не удалось сериализовать план с декомпозицией: %v", err)
		return
	}
	snap, err := e.store.Load(ctx)
	if err != nil {
		e.log.Warnf("[Checkpoint] не удалось обновить PLAN.json (Load: %v)", err)
		return
	}
	snap.PlanJSON = planJSON
	if err := e.store.Save(ctx, snap); err != nil {
		e.log.Warnf("[Checkpoint] не удалось обновить PLAN.json (Save: %v)", err)
	}
}

// writePlan записывает PLAN.md под защитой e.mu: параллельные шаги волны
// (лиды) пишут документ по очереди, без гонок на файле.
func (e *Executor) writePlan(projectName string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return writePlanDoc(projectName, e.plan)
}

// truncatedStepError обрабатывает исчерпание лимита раундов в шаге-декомпозиции
// лида. Шаг атомарен и при resume выполняется заново целиком, поэтому
// сохранённая история очищается; при отключённом чекпоинте допускается
// продолжить с частичным результатом (вернёт nil).
// clearRoundState удаляет сохранённую историю агентского цикла шага из
// чекпоинта: следующий запуск выполнит шаг заново, без застрявшего диалога.
// Сериализуется e.mu — параллельные шаги волны не должны терять обновления
// снимка чекпоинта.
func (e *Executor) clearRoundState(ctx context.Context, step *Step) {
	if e.store == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if snap, err := e.store.Load(ctx); err == nil {
		_ = e.store.ClearRoundState(ctx, snap, step.ID)
	}
}

func (e *Executor) truncatedStepError(ctx context.Context, step *Step, resp *domain.AgentResponse) error {
	if e.store != nil {
		e.clearRoundState(ctx, step)
		return fmt.Errorf("шаг %q: исчерпан лимит раундов (%d) агентского цикла — запустите с --resume, шаг выполнится заново", step.ID, resp.Rounds)
	}
	if strings.TrimSpace(resp.Content) == "" {
		return fmt.Errorf("шаг %q: модель вернула пустой ответ (исчерпан лимит раундов: %d)", step.ID, resp.Rounds)
	}
	e.log.Warnf("[%s] шаг %s: цикл исчерпал лимит раундов (%d), но чекпоинт отключён, продолжаю с частичным результатом", agentLabel(step.Agent, step.Role), step.ID, resp.Rounds)
	return nil
}

// loadResumeState читает сохранённую историю агентского цикла шага из
// чекпоинта. Возвращает состояние для продолжения цикла и true, если
// история есть. Возвращает false при resume=false, без чекпоинта или если
// истории для шага не сохранялось.
func (e *Executor) loadResumeState(ctx context.Context, stepID string) (*usecases.ResumeState, bool) {
	if e.store == nil || !e.resume {
		return nil, false
	}
	snap, err := e.store.Load(ctx)
	if err != nil {
		return nil, false
	}
	conv, ok := snap.Conversations[stepID]
	if !ok || len(conv) == 0 {
		return nil, false
	}
	var msgs []domain.Message
	if uerr := json.Unmarshal(conv, &msgs); uerr != nil {
		e.log.Warnf("[Checkpoint] повреждена сохранённая история шага %s (%v), начинаю шаг заново", stepID, uerr)
		return nil, false
	}
	return &usecases.ResumeState{Messages: msgs, Rounds: snap.Rounds[stepID]}, true
}

// persistRoundState сохраняет историю диалога и потраченные раунды шага в
// чекпоинт, чтобы при resume продолжить агентский цикл. Возвращает true,
// если состояние сохранено. Сериализуется e.mu: при параллельном выполнении
// волны истории двух шагов не должны терять обновления снимка.
func (e *Executor) persistRoundState(ctx context.Context, step *Step, resp *domain.AgentResponse) bool {
	if e.store == nil || len(resp.Messages) == 0 {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	conv, err := json.Marshal(resp.Messages)
	if err != nil {
		e.log.Warnf("[Checkpoint] ошибка сериализации истории шага %s: %v", step.ID, err)
		return false
	}
	snap, err := e.store.Load(ctx)
	if err != nil {
		e.log.Warnf("[Checkpoint] ошибка чтения чекпоинта для шага %s: %v", step.ID, err)
		return false
	}
	if err := e.store.SaveRoundState(ctx, snap, step.ID, resp.Rounds, conv); err != nil {
		e.log.Warnf("[Checkpoint] ошибка сохранения истории шага %s: %v", step.ID, err)
		return false
	}
	return true
}
