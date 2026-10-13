package usecases

import (
	"ai/domain"
	"context"
	"fmt"
	"strings"
)

// EscalationDiag — диагноз одной автономной эскалации задачи. Общий для
// зацикливания специалиста (Ф-6, этап 4.4) и «пустой работы» (гард отклонил
// done, правок/коммитов нет): у обоих одна цель — конечный бюджет попыток
// вместо бесконечного сжигания токенов.
type EscalationDiag struct {
	// Event — событие одной фразой («зациклилась», «работа не сделана»):
	// участвует во всех сообщениях (инъекция, аудит, причина остановки).
	Event string
	// Detail — причина (диагноз): видна и модели в новом прогоне, и человеку.
	Detail string
	// Action — что делать модели в новом прогоне.
	Action string
}

// EscalationStore — порт доски для эскалации. Реализуется
// environments(board.DomainStore); сужение services.BoardStore до методов,
// которые реально нужны use-case (интерфейс принадлежит внутреннему слою).
type EscalationStore interface {
	GetTask(ctx context.Context, id string) (*domain.Task, error)
	PatchTask(ctx context.Context, id string, mutate func(*domain.Task) error) error
	SetTaskStatus(ctx context.Context, id string, status domain.Status) error
	AddTaskComment(ctx context.Context, taskID string, comment domain.Comment) ([]domain.Comment, error)
}

// EscalationPorts — внешние зависимости эскалации (инфраструктура: доска,
// лог/чат, окружение).
type EscalationPorts struct {
	// Tasks — хранилище доски.
	Tasks EscalationStore
	// MaxEscalations — бюджет автономии: сколько раз задача может получить
	// эскалацию (сильная модель + инъекция с диагнозом), прежде чем работа
	// будет остановлена и отдана человеку. Перекрывается KANBAN_MAX_ESCALATIONS
	// на стороне инфраструктуры.
	MaxEscalations int
	// Status — аудит решения в чат/лог проекта (nil — только логируем хранилище).
	Status func(format string, args ...any)
	// SetStopReason — причина остановки работы по вине человека.
	SetStopReason func(reason string)
}

// Escalate реагирует на неудачную попытку задачи (зацикливание или работа
// без результата) в рамках бюджета автономии. Возвращает true, если работа
// продвинулась (задача поставлена в очередь на новый прогон с эскалацией или
// остановлена для человека) — это честный «прогресс» раунда, иначе цикл решил
// бы, что прогресса нет, и упал.
//
// Пока бюджет не исчерпан: пишем в задачу требование большой модели
// (model_tier), инъекцию с диагнозом (причина видна модели с первого запроса
// нового прогона) и возвращаем задачу в очередь (ready). Откат кода при этом
// НЕ делается (Р-2).
//
// Исчерпали бюджет: останавливаем работу задачи и зовём человека — «помощь
// человека» (status human_help, не терминальный: вернуть в очередь можно
// одной кнопкой) плюс явное сообщение в чат и причина остановки в задаче.
// Никаких «выполнено»/фиктивного успеха.
func Escalate(ctx context.Context, taskID string, diag EscalationDiag, p EscalationPorts) (bool, error) {
	if p.Tasks == nil {
		return false, fmt.Errorf("задача %s: эскалация без хранилища", taskID)
	}
	status := p.Status
	if status == nil {
		status = func(string, ...any) {}
	}
	setStopReason := p.SetStopReason
	if setStopReason == nil {
		setStopReason = func(string) {}
	}

	t, err := p.Tasks.GetTask(ctx, taskID)
	if err != nil {
		return false, fmt.Errorf("задача %s: чтение для эскалации: %w", taskID, err)
	}
	if t == nil {
		return false, fmt.Errorf("задача %s не найдена", taskID)
	}

	budget := p.MaxEscalations
	if t.Escalations >= budget {
		// Причина остановки оседает в задаче (карточка в UI): человек должен
		// увидеть, ЧТО именно не получилось, а не «загадочный простой».
		reasonFull := fmt.Sprintf("Бюджет автономии исчерпан (%d эскалаций): %s. %s. Действие: %s",
			t.Escalations, diag.Event, ClipText(diag.Detail, 400), diag.Action)
		if err := p.Tasks.PatchTask(ctx, taskID, func(cur *domain.Task) error {
			cur.PauseReason = ClipText(reasonFull, 800)
			return nil
		}); err != nil {
			return false, fmt.Errorf("задача %s: запись причины остановки: %w", taskID, err)
		}
		if err := p.Tasks.SetTaskStatus(ctx, taskID, domain.StatusHumanHelp); err != nil {
			return false, fmt.Errorf("задача %s: остановка исчерпанием бюджета: %w", taskID, err)
		}
		_, _ = p.Tasks.AddTaskComment(ctx, taskID, domain.Comment{
			Author: "system",
			Type:   domain.CommentTypeSystem,
			Body:   fmt.Sprintf("Форсмажор: %s\n\nДиагноз: %s\n\nАнализ/рекомендация: %s", diag.Event, diag.Detail, diag.Action),
		})
		stop := fmt.Sprintf("задача %s: %s — попытка %d, бюджет автономии исчерпан: работа остановлена, нужен человек (последняя причина: %s)",
			t.TaskID, diag.Event, t.Escalations+1, ClipText(diag.Detail, 160))
		setStopReason(stop)
		status("[задача %s] %s %d раз(а) подряд (последняя причина: %s) — бюджет автономии исчерпан, работа остановлена, нужен человек",
			t.TaskID, diag.Event, t.Escalations+1, ClipText(diag.Detail, 160))
		return true, nil
	}

	escalation := t.Escalations + 1
	if err := p.Tasks.PatchTask(ctx, taskID, func(cur *domain.Task) error {
		cur.ModelTier = domain.ModelTierLarge
		cur.Escalations = escalation
		// Диагноз как инъекция: новая модель/новый прогон видят причину
		// сразу, а не заново наступают на тот же грабли. Инъекция уходит в
		// конец блока сообщений (append) — как требование «что делать дальше»,
		// а не как подмена системного промпта.
		inj := domain.Injection{
			Name:   fmt.Sprintf("%s (эскалация %d/%d)", diag.Event, escalation, budget),
			Scope:  domain.InjectionScopeTask,
			Target: domain.InjectionTargetUserLast,
			Content: fmt.Sprintf("Предыдущая попытка: %s (эскалация %d/%d). Причина: %s. %s",
				diag.Event, escalation, budget, ClipText(diag.Detail, 400), diag.Action),
		}
		inj.Normalize()
		cur.Injections = append(cur.Injections, inj)
		return nil
	}); err != nil {
		return false, fmt.Errorf("задача %s: запись эскалации: %w", taskID, err)
	}
	if err := p.Tasks.SetTaskStatus(ctx, taskID, domain.StatusReady); err != nil {
		return false, fmt.Errorf("задача %s: возврат в очередь после эскалации: %w", taskID, err)
	}
	status("[задача %s] %s (%s) — эскалация %d/%d: продолжаем на большой модели с разбором причины",
		t.TaskID, diag.Event, ClipText(diag.Detail, 160), escalation, budget)
	return true, nil
}

// ClipText обрезает длинную строку для читаемых сообщений эскалации
// (как planner.truncateText, но в слое application): пробелы по краям убираются,
// длинный хвост заменяется на «…».
func ClipText(s string, n int) string {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) <= n {
		return trimmed
	}
	return trimmed[:n] + "…"
}
