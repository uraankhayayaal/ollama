package planner

import (
	"ai/application/usecases"
	"ai/domain"
	"ai/domain/services"
	"ai/shared/tokens"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// phaseExecute: раздаёт готовые задачи специалистам. Правило «одна задача на
// одного специалиста»: за цикл каждый специалист (assignee) получает не более
// одной задачи. Задача: готова к работе -> в работе; после успешного цикла
// агента -> выполнена.
func (k *KanbanRunner) phaseExecute(ctx context.Context) (bool, error) {
	ready, err := k.readyTasks(ctx)
	if err != nil {
		return false, err
	}
	if len(ready) == 0 {
		return false, nil
	}

	busy := map[string]bool{}
	progress := false
	for _, t := range ready {
		// Одна задача на одного специалиста за цикл.
		if busy[t.Assignee] {
			k.log.Detailf("[задача %s] ждёт: специалист %s уже занят в этом цикле", t.TaskID, t.Assignee)
			continue
		}
		busy[t.Assignee] = true

		// Эпик, в котором появилась работающая задача, тоже уходит «в работу».
		k.noteEpicProgress(ctx, t.EpicID)

		specialist, err := k.specialistFor(t)
		if err != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, err)
		}
		// Специалисты получают доступ к доске: смена статуса своей задачи.
		if sb, ok := specialist.(interface {
			SetBoardStore(services.BoardStore)
		}); ok {
			sb.SetBoardStore(k.store)
		}
		// Специалист git-проекта работает в своём worktree ветки задачи (Ф-3):
		// правки падают в ветку, авто-коммит на done подхватит их. В остальных
		// случаях остаётся стандартный OutputDir (temp/<проект>).
		if k.outputDir != nil {
			if dir := k.outputDir(k.store.Project(), t.TaskID); dir != "" {
				if so, ok := specialist.(interface{ SetOutputDir(string) }); ok {
					so.SetOutputDir(dir)
					k.log.Infof("[задача %s] рабочая директория → %s", t.TaskID, dir)
				}
			}
		}
		// Имя проекта для RAG — всегда до worktree: имя каталога задачи
		// (.wt-task-<проект>-<id>) в индексе не совпадает с именем проекта,
		// и без явного значения CodeSearch идёт по пустому «проекту».
		if k.store != nil {
			if sn, ok := specialist.(interface{ SetProjectName(string) }); ok {
				sn.SetProjectName(k.store.Project())
			}
		}
		// Ф-6 (этап 2): id своей задачи — агенту нужен, чтобы писать
		// состояние раунда (State Tracking) в нужную запись доски.
		if ti, ok := specialist.(interface{ SetTaskID(string) }); ok {
			ti.SetTaskID(t.TaskID)
		}

		k.log.Infof("[задача %s] специалист %s выполняет: %s",
			t.TaskID, t.Assignee, truncateText(t.Title, 60))
		// Промпт-инъекции задачи: снапшот на старте + ЖИВОЙ источник, который
		// перечитывает задачу перед каждым запросом к модели. Благодаря живому
		// источнику правка инъекций (REST/инструмент доски) видна модели со
		// следующего раунда, даже если цикл уже идёт. Также передаём комментарии
		// в контекст для будущей доставки в модель.
		//
		// Контекст собирается в taskCtx, а не в ctx: присваивание в ctx внутри
		// цикла по ready оставляло бы контекст предыдущей задачи следующей.
		taskCtx := domain.NewInjectionContext(ctx, t.Injections)
		taskCtx = domain.NewCommentContext(taskCtx, t.Comments)
		if k.store != nil {
			taskCtx = domain.WithInjectionSource(taskCtx, func(ctx context.Context) ([]domain.Injection, error) {
				fresh, err := k.store.GetTask(ctx, t.TaskID)
				if err != nil {
					return nil, err // доска недоступна — работаем со снапшотом
				}
				if fresh == nil {
					return nil, fmt.Errorf("задача %s не найдена", t.TaskID)
				}
				return fresh.Injections, nil
			})
			taskCtx = domain.WithCommentSource(taskCtx, func(ctx context.Context) (domain.Comments, error) {
				fresh, err := k.store.GetTask(ctx, t.TaskID)
				if err != nil {
					return nil, err
				}
				if fresh == nil {
					return nil, fmt.Errorf("задача %s не найдена", t.TaskID)
				}
				return fresh.Comments, nil
			})
		}
		taskCtx = domain.WithInjectionScope(taskCtx, domain.InjectionScope{
			Project: k.store.Project(),
			TaskID:  t.TaskID,
			Role:    t.Assignee,
		})
		// Требование «эта задача тяжёлая» из доски (Ф-6, этап 4.3): смена модели
		// переживает рестарт сервера, потому что живёт в записи задачи, а не
		// внутри одного вызова Generate.
		if t.ModelTier == domain.ModelTierLarge {
			taskCtx = domain.WithHeavyModel(taskCtx)
			k.log.Detailf("[задача %s] модель: LARGE (требование задачи)", t.TaskID)
		}

		k.markActive(t.TaskID)
		chatName := agentChatNameForKanban(specialist, t.Assignee)
		if rep := services.ReporterFromContext(taskCtx); rep != nil {
			taskCtx = services.WithReporter(taskCtx, rep.WithAgent(chatName))
		}
		resp, genErr := k.generate(taskCtx, tokens.ScopeTask(t.TaskID), specialist)
		k.unmarkActive(t.TaskID)
		if genErr != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, genErr)
		}
		// Специалист зациклился: разрыв петли не помог, модель не поняла свою
		// ошибку и повторяет то же самое. Задачу НЕ помечаем выполненной, но и
		// запуск НЕ роняем (Ф-6, этап 4.4): эскалируем — сильная модель плюс
		// инъекция с диагнозом — и возвращаем задачу в очередь на новый прогон.
		// Отката кода здесь нет намеренно (Р-2): отменённый прогон не
		// «улучшает» задачу, он просто тратит токены.
		if resp != nil && resp.Looped {
			done, err := k.escalateLoop(ctx, t, resp.LoopReason)
			if err != nil {
				return false, err
			}
			// Ни успеха, ни провала раунда: следующий раунд подхватит задачу
			// снова (фаза исполнит её с требованием LARGE).
			progress = progress || done
			continue
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("задача %s: цикл остановлен по лимиту раундов", t.TaskID)
		}

		// Разработчик сам переводит задачу в done по завершении работы
		// (BoardSetTaskStatus). Проверяем фактический статус и, если агент
		// не сменил его, — страхуем fallback-ом оркестратора, чтобы задача
		// не зависла и канал не зацикливался.
		current, err := k.store.GetTask(ctx, t.TaskID)
		if err != nil {
			return false, fmt.Errorf("задача %s: чтение статуса после работы: %w", t.TaskID, err)
		}
		if current.Status == domain.StatusDone {
			k.log.Infof("[задача %s] в работе → выполнена (агент подтвердил сам)", t.TaskID)
			if err := k.finalizeTaskTokens(ctx, t.TaskID); err != nil {
				k.log.Warnf("[учёт токенов] задача %s: %v", t.TaskID, err)
			}
			progress = true
			continue
		}
		// Специалист сам снял задачу с работы (cancelled). Осознанный шаг
		// агента: уважаем статус, сообщаем человеку.
		if current.Status == domain.StatusCancelled {
			reason := truncateText(current.PauseReason, 400)
			if reason == "" {
				reason = "причина не указана"
			}
			k.log.Warnf("[задача %s] специалист снял с работы (cancelled): %s", t.TaskID, reason)
			k.status("[задача %s] специалист снял с работу: %s", t.TaskID, reason)
			progress = true
			continue
		}
		// Разработчик завершил раунд, но не сменил статус (непонятный исход) —
		// эскалируем: задача возвращается в очередь с диагнозом.
		if _, escErr := k.escalate(ctx, current, escalationDiag{
			Event:  "работа не сделана",
			Detail: "агент не сменил статус задачи",
			Action: "Сделай работу по существу — внеси правки в проект (файлы в worktree задачи) и зафиксируй их коммитом. Если задача невыполнима — переведи её в cancelled с обязательной причиной.",
		}); escErr != nil {
			return false, fmt.Errorf("задача %s: эскалация пустой работы: %w", t.TaskID, escErr)
		}
		progress = true
	}
	return progress, nil
}

// maxEscalationsDefault — бюджет автономии: сколько раз задача может получить
// эскалацию (сильная модель + инъекция с диагнозом), прежде чем работа будет
// остановлена и отдана человеку. Считаются и зацикливания, и «пустые»
// отклонения гарда phantom-done (FEL-04): без конечного бюджета оба
// превращаются в бесконечный цикл дорогих прогонов. Перекрывается
// KANBAN_MAX_ESCALATIONS.
const maxEscalationsDefault = 3

func maxEscalations() int {
	if v := strings.TrimSpace(os.Getenv("KANBAN_MAX_ESCALATIONS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return maxEscalationsDefault
}

// escalationDiag — диагноз одной автономной эскалации задачи: политика и
// применение живут в application/usecases (usecases.Escalate), здесь только
// алиас для совместимости точек вызова.
type escalationDiag = usecases.EscalationDiag

// escalate реагирует на неудачную попытку задачи (зацикливание или работа
// без результата) в рамках бюджета автономии. Возвращает true, если работа
// продвинулась (задача поставлена в очередь на новый прогон с эскалацией) —
// это честный «прогресс» раунда, иначе цикл решил бы, что прогресса нет, и
// упал.
//
// Реализация — usecases.Escalate (application): слой принимает решение
// (бюджет, инъекция с диагнозом, human_help при исчерпании), а здесь — только
// адаптеры инфраструктуры (доска через services.BoardStore, лог/чат, бюджет из
// окружения).
func (k *KanbanRunner) escalate(ctx context.Context, t *domain.Task, diag escalationDiag) (bool, error) {
	return usecases.Escalate(ctx, t.TaskID, diag, usecases.EscalationPorts{
		Tasks:          k.store,
		MaxEscalations: maxEscalations(),
		Status:         k.status,
		SetStopReason:  k.setStopReason,
	})
}

// escalateLoop реагирует на зацикливание специалиста (Ф-6, этапы 4.4–4.5).
func (k *KanbanRunner) escalateLoop(ctx context.Context, t *domain.Task, reason string) (bool, error) {
	return k.escalate(ctx, t, escalationDiag{
		Event:  "зациклилась",
		Detail: reason,
		Action: "Не повторяй те же действия: перечитай задачу, измени подход и проверь результат.",
	})
}

// forceMajeure — форсмажор фазы: отмена задачи + запись причины.
// В отличие от эскалации петли (escalateLoop) здесь нет задачи для повторного
// прогона: работа не состоялась, и человек должен решить, что делать.
// Если taskID задан — комментарий с причиной крепится к задаче и её
// статус уходит в cancelled; без taskID помечается мета-задача доски.
// Ошибки записи не маскируют исходный сбой: только логируются.
func (k *KanbanRunner) forceMajeure(ctx context.Context, taskID, reason string) {
	msg := fmt.Sprintf("форс-мажор: %s", truncateText(reason, 400))
	k.setStopReason(msg)

	if taskID != "" {
		if _, err := k.store.AddTaskComment(ctx, taskID, domain.Comment{
			Author: "system",
			Type:   domain.CommentTypeSystem,
			Body:   "Форсмажор (непредвиденный случай): " + reason + ". Нужно решение человека: разбери причину (логи проекта, вывод проверок) и при необходимости создай новую задачу.",
		}); err != nil {
			k.log.Warnf("[доска] форс-мажор задачи %s: комментарий: %v", taskID, err)
		}
		_ = k.store.PatchTask(ctx, taskID, func(cur *domain.Task) error {
			if cur.PauseReason == "" {
				cur.PauseReason = truncateText(reason, 800)
			}
			return nil
		})
		if err := k.store.SetTaskStatus(ctx, taskID, domain.StatusCancelled); err != nil {
			k.log.Warnf("[доска] форс-мажор задачи %s: статус cancelled: %v", taskID, err)
		}
	} else if meta, err := k.store.GetMeta(ctx); err == nil && meta != nil && !meta.Status.Terminal() {
		meta.Status = domain.StatusCancelled
		if err := k.store.SaveMeta(ctx, meta); err != nil {
			k.log.Warnf("[доска] форс-мажор: статус meta cancelled: %v", err)
		}
	}
	k.status("[доска] %s — работа остановлена, нужна помощь человека", msg)
}

// architectFail — завершение фазы архитектора по форсмажору (4.5): человеку
// уходит «Помощь человека» с причиной, а исходная ошибка возвращается вызывающему
// (цикл остановится по ней — фиктивного успеха нет). Отмена контекста (останов
// сессии человеком) не форсмажор: просто пробрасываем ошибку как есть.
// Возвращает err без изменений — можно писать `return k.architectFail(ctx, err)`.
func (k *KanbanRunner) architectFail(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return err
	}
	k.forceMajeure(ctx, "", err.Error())
	return err
}

// agentChatNameForKanban возвращает человекочитаемое имя агента для чата.
func agentChatNameForKanban(spec services.Agent, assignee string) string {
	type labler interface{ Label() string }
	if l, ok := spec.(labler); ok && l.Label() != "" {
		return l.Label()
	}
	r := strings.ToLower(assignee)
	switch {
	case strings.Contains(r, "qa") && strings.Contains(r, "lead"):
		return "QA-лид"
	case strings.Contains(r, "qa"), strings.Contains(r, "тест"), strings.Contains(r, "testing"):
		return "QA-инженер"
	case strings.Contains(r, "devopslead") || (strings.Contains(r, "lead") && (strings.Contains(r, "devops") || strings.Contains(r, "инфра") || strings.Contains(r, "infra"))):
		return "DevOps-лид"
	case strings.Contains(r, "devops"), strings.Contains(r, "инфра"), strings.Contains(r, "infra"), strings.Contains(r, "sre"), strings.Contains(r, "ci"):
		return "DevOps-инженер"
	case strings.Contains(r, "frontendlead") || (strings.Contains(r, "lead") && (strings.Contains(r, "front") || strings.Contains(r, "фронт") || strings.Contains(r, "ui"))):
		return "Frontend-лид"
	case strings.Contains(r, "backendlead") || (strings.Contains(r, "lead") && (strings.Contains(r, "back") || strings.Contains(r, "бэк"))):
		return "Backend-лид"
	case strings.Contains(r, "architect"):
		return "архитектор"
	case strings.Contains(r, "front"), strings.Contains(r, "react"), strings.Contains(r, "vue"), strings.Contains(r, "next"), strings.Contains(r, "ui"), strings.Contains(r, "фронт"):
		return "фронтенд-разработчик"
	case strings.Contains(r, "back"), strings.Contains(r, "бэк"), strings.Contains(r, "backend"), strings.Contains(r, "go"), strings.Contains(r, "python"), strings.Contains(r, "java"), strings.Contains(r, "node"):
		return "бэкенд-разработчик"
	}
	return assignee
}
