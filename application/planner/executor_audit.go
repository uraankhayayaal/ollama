package planner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"ai/domain/services"
	"ai/projects"
)

// trackStepMetrics обновляет метрики шага (теляметрия стадий: раунды, вердикт
// приёмки, изменения контракта, изменённые файлы) в чекпоинте И в едином
// реестре телеметрии (Ф-1). Оба канала независимы: чекпоинт переживает resume,
// реестр отдаёт метрики в /metrics и дашборд. На ход плана не влияет.
func (e *Executor) trackStepMetrics(ctx context.Context, stepID string, values map[string]any) {
	if reg := e.metrics; reg != nil {
		for k, v := range values {
			if f, ok := metricFloat(v); ok {
				reg.SetStepMetric(e.metricScope(), stepID, k, f)
			}
		}
	}
	if e.store == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if snap, err := e.store.Load(ctx); err == nil {
		if err := e.store.TrackMetric(ctx, snap, stepID, values); err != nil {
			e.log.Detailf("[Checkpoint] ошибка сохранения метрик шага %s: %v", stepID, err)
		}
	}
}

// metricScope — scope для метрик шага в реестре: имя проекта плана, иначе
// «plan». Плоский scope осознан: шаги консольного плана не относятся к задаче
// или эпику доски, а выдумывать для них идентификатор единицы работы не нужно.
func (e *Executor) metricScope() string {
	if e.plan != nil && e.plan.ProjectName != "" {
		return e.plan.ProjectName
	}
	return "plan"
}

// metricFloat приводит значение метрики к числу. Чекпоинт хранит map[string]any
// (там значения приходят из разных мест), а реестр оперирует числами: нечисловые
// значения молча пропускаются, а не превращаются в нули.
func metricFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// rollbackEnabled — включает/выключает защитный откат упавших шагов
// переменной CODEGEN_ROLLBACK_ON_FAIL. По умолчанию ВКЛЮЧЕН: неудачный шаг
// субагента не должен оставлять в общем репозитории изменённый/удалённый чужой
// код или половину переписанного файла. Отключается значением "0"/"false"/
// "off" — например, когда откат мешает отладке или обрезает полезный прогресс.
func rollbackEnabled() bool {
	if v := strings.TrimSpace(os.Getenv("CODEGEN_ROLLBACK_ON_FAIL")); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "off", "no":
			return false
		}
	}
	return true
}

// contractAuditEnabled — снимать снимок публичной поверхности проекта до/после
// шага разработчика (сравнение нормализованных сигнатур). Отключение/включение
// через CODEGEN_CONTRACT_ENFORCE (0/1); по умолчанию снимок снимается и diff
// пишется в Detail, при enforce=1 уровень повышается до предупреждения.
func contractAuditEnabled() bool {
	if v := strings.TrimSpace(os.Getenv("CODEGEN_CONTRACT_ENFORCE")); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "off", "no":
			return false
		}
	}
	return true
}

// auditContractStep сравнивает публичную поверхность проекта до и после шага
// разработчика и логирует изменения контракта (добавленные/удалённые/
// изменённые сигнатуры). Это диагностика, а не блокирующая проверка: вердикт
// об ошибке шага выносят приёмка и анализатор (acceptor).
func (e *Executor) auditContractStep(ctx context.Context, step *Step, projectName string, before *services.PublicAPISnapshot) {
	if e.apiAuditor == nil {
		return
	}
	after, err := e.apiAuditor.Snapshot(projects.ProjectDir(projectName))
	if err != nil {
		e.log.Detailf("[%s] шаг %s: не удалось снять повторный снимок API: %v", agentLabel(step.Agent, step.Role), step.ID, err)
		return
	}
	diff := e.apiAuditor.Compare(before, after)
	if len(diff) == 0 {
		return
	}
	e.trackStepMetrics(ctx, step.ID, map[string]any{"contract_changes": len(diff)})
	constrained := diff
	if len(constrained) > 12 {
		rest := fmt.Sprintf("…и ещё %d деклараций", len(constrained)-12)
		constrained = append(constrained[:12], rest)
	}
	enforced := strings.TrimSpace(os.Getenv("CODEGEN_CONTRACT_ENFORCE")) != ""
	if enforced {
		e.log.Warnf("[%s] шаг %s: ИЗМЕНЕНИЕ КОНТРАКТА (публичная поверхность): %d деклараций:\n  %s", agentLabel(step.Agent, step.Role), step.ID, len(diff), strings.Join(constrained, "\n  "))
		return
	}
	e.log.Detailf("[%s] шаг %s: изменение контракта (публичная поверхность): %d деклараций:\n  %s", agentLabel(step.Agent, step.Role), step.ID, len(diff), strings.Join(constrained, "\n  "))
}

// auditFileChanges выводит дайджест файловых правок шага (что субагент
// создал/изменил/удалил) и сохраняет счётчики в метрики чекпоинта. Ничего не
// откатывает — это аудит, а не контроль: код на всякий случай не трогается.
func (e *Executor) auditFileChanges(ctx context.Context, step *Step, snap services.Snapshot) {
	added, modified, removed, err := snap.Diff()
	if err != nil {
		e.log.Detailf("[%s] шаг %s: не удалось вычислить diff правок: %v", agentLabel(step.Agent, step.Role), step.ID, err)
		return
	}
	total := len(added) + len(modified) + len(removed)
	if total == 0 {
		return
	}
	e.trackStepMetrics(ctx, step.ID, map[string]any{"files_added": len(added), "files_modified": len(modified), "files_removed": len(removed)})
	limit := func(paths []string) string {
		if len(paths) > 8 {
			return strings.Join(paths[:8], ", ") + fmt.Sprintf(" … и ещё %d", len(paths)-8)
		}
		return strings.Join(paths, ", ")
	}
	e.log.Detailf("[%s] шаг %s: правки субагента: +%d создано, ~%d изменено, -%d удалено", agentLabel(step.Agent, step.Role), step.ID, len(added), len(modified), len(removed))
	if len(added) > 0 {
		e.log.Detailf("[%s] шаг %s:   создано: %s", agentLabel(step.Agent, step.Role), step.ID, limit(added))
	}
	if len(modified) > 0 {
		e.log.Detailf("[%s] шаг %s:   изменено: %s", agentLabel(step.Agent, step.Role), step.ID, limit(modified))
	}
	if len(removed) > 0 {
		e.log.Detailf("[%s] шаг %s:   удалено: %s", agentLabel(step.Agent, step.Role), step.ID, limit(removed))
	}
}

// rollbackStep откатывает директорию проекта к снимку, сделанному перед
// началом выполнения шага: восстанавливает изменённые/удалённые файлы и
// удаляет созданные субагентом. Вызывается ТОЛЬКО при реальной ошибке шага
// (не при исчерпании раундов/обрезке по токенам — там откат поломал бы resume:
// сохранённая история диалога ссылается на уже существующие файлы).
//
// Возвращает true, если откат реально что-то изменил.
func (e *Executor) rollbackStep(projectName, stepID string, snap services.Snapshot) bool {
	if snap == nil || !rollbackEnabled() {
		return false
	}
	restored, removed, err := snap.Restore()
	if err != nil {
		e.log.Warnf("[откат] шаг %s: не удалось откатить директорию %s: %v", stepID, projectName, err)
		return false
	}
	if len(restored) == 0 && len(removed) == 0 {
		e.log.Detailf("[откат] шаг %s: снимок актуален, правок не потребовалось", stepID)
		return false
	}
	if len(restored) > 0 {
		e.log.Warnf("[откат] шаг %s: восстановлено файлов: %d (изменённые/удалённые субагентом): %v", stepID, len(restored), restored)
	}
	if len(removed) > 0 {
		e.log.Warnf("[откат] шаг %s: удалено файлов, созданных упавшим субагентом: %d: %v", stepID, len(removed), removed)
	}
	return true
}
