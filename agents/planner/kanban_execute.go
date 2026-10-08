package planner

import (
	"ai/board"
	"ai/models"
	"ai/tokens"
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

		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusInProgress); err != nil {
			return false, fmt.Errorf("задача %s: готова к работе -> в работе: %w", t.TaskID, err)
		}
		// Эпик, в котором появилась работающая задача, тоже уходит «в работу».
		k.noteEpicProgress(ctx, t.EpicID)

		specialist, err := k.specialistFor(t)
		if err != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, err)
		}
		// Специалисты получают доступ к доске: смена статуса своей задачи,
		// QA-инженер — публикация багрепортов (BoardCreateBugReport).
		if sb, ok := specialist.(interface{ SetBoardStore(*board.Store) }); ok {
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
		taskCtx := board.NewInjectionContext(ctx, t.Injections)
		taskCtx = board.NewCommentContext(taskCtx, t.Comments)
		if k.store != nil {
			taskCtx = board.WithInjectionSource(taskCtx, func(ctx context.Context) ([]board.Injection, error) {
				fresh, err := k.store.GetTask(ctx, t.TaskID)
				if err != nil {
					return nil, err // доска недоступна — работаем со снапшотом
				}
				if fresh == nil {
					return nil, fmt.Errorf("задача %s не найдена", t.TaskID)
				}
				return fresh.Injections, nil
			})
			taskCtx = board.WithCommentSource(taskCtx, func(ctx context.Context) (board.Comments, error) {
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
		taskCtx = board.WithInjectionScope(taskCtx, board.InjectionScope{
			Project: k.store.Project(),
			TaskID:  t.TaskID,
			Role:    t.Assignee,
		})
		// Требование «эта задача тяжёлая» из доски (Ф-6, этап 4.3): смена модели
		// переживает рестарт сервера, потому что живёт в записи задачи, а не
		// внутри одного вызова Generate.
		if t.ModelTier == board.ModelTierLarge {
			taskCtx = models.WithHeavyModel(taskCtx)
			k.log.Detailf("[задача %s] модель: LARGE (требование задачи)", t.TaskID)
		}

		k.markActive(t.TaskID)
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
		if current.Status == board.StatusDone {
			k.log.Infof("[задача %s] в работе → выполнена (агент подтвердил сам)", t.TaskID)
			// Ф-3: факт расхода токенов задачи фиксируется сразу после выполнения.
			if err := k.finalizeTaskTokens(ctx, t.TaskID); err != nil {
				k.log.Warnf("[учёт токенов] задача %s: %v", t.TaskID, err)
			}
			progress = true
			continue
		}
		if current.Status == board.StatusTesting {
			k.log.Infof("[задача %s] в работе → на тестирование (агент подтвердил сам)", t.TaskID)
			if err := k.finalizeTaskTokens(ctx, t.TaskID); err != nil {
				k.log.Warnf("[учёт токенов] задача %s: %v", t.TaskID, err)
			}
			progress = true
			continue
		}
		// Специалист сам вывел задачу из работы: human_help (задача
		// невыполнима — нет исходного кода/зависимостей, блокер, зацикливание)
		// или cancelled (снята с работы). Это осознанный шаг агента, а не
		// «пустая попытка»: уважаем статус, сообщаем человеку и НЕ страхуем
		// fallback-ом в done — гард всё равно бы отказал, а эскалация (ниже)
		// затёрла бы решение специалиста: оба статуса нетерминальны, вернуть
		// задачу в очередь можно одной кнопкой (или докомандой лиду).
		if current.Status == board.StatusHumanHelp || current.Status == board.StatusCancelled {
			reason := truncateText(current.PauseReason, 400)
			if reason == "" {
				reason = "причина не указана"
			}
			what := "перевёл в human_help (просит помощь человека)"
			if current.Status == board.StatusCancelled {
				what = "снял с работы (cancelled)"
			}
			k.log.Warnf("[задача %s] специалист %s: %s", t.TaskID, what, reason)
			k.status("[задача %s] специалист %s — задача ждёт решения человека: %s", t.TaskID, what, reason)
			progress = true
			continue
		}
		// Разработчик завершил раунд, но не сменил статус (непонятный исход) —
		// по Этапу 6: переводим задачу в «На тестирование» (testing).
		// «Отказ гарда» (переход отклонён) трактуем как невозможность сдать задачу:
		// эскалируем в human_help с комментарием и постановкой причины.
		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusTesting); err != nil {
			k.log.Warnf("[задача %s] переход в testing отклонён: %v", t.TaskID, err)
			if _, perr := k.store.AddTaskComment(ctx, t.TaskID, board.Comment{
				Author: "system",
				Type:   board.CommentTypeSystem,
				Body:   fmt.Sprintf("Авто-фолбэк: не удалось перевести задачу в «На тестирование» (testing). Причина: %s", truncateText(err.Error(), 400)),
			}); perr != nil {
				k.log.Warnf("[задача %s] комментарий к задаче: %v", t.TaskID, perr)
			}
			if _, escErr := k.escalate(ctx, current, escalationDiag{
				event:  "работа не сделана",
				detail: err.Error(),
				action: "Сделай работу по существу — внеси правки в проект (файлы в worktree задачи) и зафиксируй их коммитом. Если задача невыполнима (нужного кода, зависимостей или доступа в проекте нет) — переведи её в human_help инструментом BoardSetTaskStatus с обязательной причиной (reason).",
			}); escErr != nil {
				return false, fmt.Errorf("задача %s: эскалация пустой работы: %w", t.TaskID, escErr)
			}
			progress = true
			continue
		}
		k.log.Infof("[задача %s] в работе → на тестирование (fallback: агент не сменил статус)", t.TaskID)
		if err := k.finalizeTaskTokens(ctx, t.TaskID); err != nil {
			k.log.Warnf("[учёт токенов] задача %s: %v", t.TaskID, err)
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

// escalationDiag — диагноз одной автономной эскалации задачи. Общий для
// зацикливания (Ф-6, этап 4.4) и «пустой работы» (инцидент FEL-04: гард
// отклонил done, правок/коммитов нет): у обоих одна цель — конечный бюджет
// попыток вместо бесконечного сжигания токенов.
type escalationDiag struct {
	// event — событие одной фразой («зациклилась», «работа не сделана»):
	// участвует во всех сообщениях (инъекция, аудит, причина остановки).
	event string
	// detail — причина (диагноз): видна и модели в новом прогоне, и человеку.
	detail string
	// action — что делать модели в новом прогоне.
	action string
}

// escalate реагирует на неудачную попытку задачи (зацикливание или работа
// без результата) в рамках бюджета автономии. Возвращает true, если работа
// продвинулась (задача поставлена в очередь на новый прогон с эскалацией) —
// это честный «прогресс» раунда, иначе цикл решил бы, что прогресса нет, и
// упал.
//
// Пока бюджет не исчерпан: пишем в задачу требование большой модели
// (model_tier), инъекцию с диагнозом (причина видна модели с первого запроса
// нового прогона) и возвращаем задачу в очередь. Откат кода при этом НЕ
// делается (Р-2).
//
// Исчерпали бюджет: останавливаем работу задачи и зовём человека — «помощь
// человека» (не терминальный статус: вернуть в очередь можно одной кнопкой)
// плюс явное сообщение в чат. Никаких «выполнено»/фиктивного успеха.
func (k *KanbanRunner) escalate(ctx context.Context, t *board.Task, diag escalationDiag) (bool, error) {
	budget := maxEscalations()
	if t.Escalations >= budget {
		// Причина остановки оседает в задаче (карточка в UI): человек должен
		// увидеть, ЧТО именно не получилось, а не «загадочный простой».
		reasonFull := fmt.Sprintf("Бюджет автономии исчерпан (%d эскалаций): %s. %s. Действие: %s",
			t.Escalations, diag.event, truncateText(diag.detail, 400), diag.action)
		if err := k.store.PatchTask(ctx, t.TaskID, func(cur *board.Task) error {
			cur.PauseReason = truncateText(reasonFull, 800)
			return nil
		}); err != nil {
			return false, fmt.Errorf("задача %s: запись причины остановки: %w", t.TaskID, err)
		}
		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusHumanHelp); err != nil {
			return false, fmt.Errorf("задача %s: остановка исчерпанием бюджета: %w", t.TaskID, err)
		}
		_, _ = k.store.AddTaskComment(ctx, t.TaskID, board.Comment{
			Author: "system",
			Type:   board.CommentTypeSystem,
			Body:   fmt.Sprintf("Форсмажор: %s\n\nДиагноз: %s\n\nАнализ/рекомендация: %s", diag.event, diag.detail, diag.action),
		})
		stop := fmt.Sprintf("задача %s: %s — попытка %d, бюджет автономии исчерпан: работа остановлена, нужен человек (последняя причина: %s)",
			t.TaskID, diag.event, t.Escalations+1, truncateText(diag.detail, 160))
		k.setStopReason(stop)
		k.status("[задача %s] %s %d раз(а) подряд (последняя причина: %s) — бюджет автономии исчерпан, работа остановлена, нужен человек",
			t.TaskID, diag.event, t.Escalations+1, truncateText(diag.detail, 160))
		return true, nil
	}

	escalation := t.Escalations + 1
	if err := k.store.PatchTask(ctx, t.TaskID, func(cur *board.Task) error {
		cur.ModelTier = board.ModelTierLarge
		cur.Escalations = escalation
		// Диагноз как инъекция: новая модель/новый прогон видят причину
		// сразу, а не заново наступают на тот же грабли. Инъекция уходит в
		// конец блока сообщений (append) — как требование «что делать дальше»,
		// а не как подмена системного промпта.
		inj := board.Injection{
			Name:   fmt.Sprintf("%s (эскалация %d/%d)", diag.event, escalation, budget),
			Scope:  board.InjectionScopeTask,
			Target: board.InjectionTargetUserLast,
			Content: fmt.Sprintf("Предыдущая попытка: %s (эскалация %d/%d). Причина: %s. %s",
				diag.event, escalation, budget, truncateText(diag.detail, 400), diag.action),
		}
		inj.Normalize()
		cur.Injections = append(cur.Injections, inj)
		return nil
	}); err != nil {
		return false, fmt.Errorf("задача %s: запись эскалации: %w", t.TaskID, err)
	}
	if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusReady); err != nil {
		return false, fmt.Errorf("задача %s: возврат в очередь после эскалации: %w", t.TaskID, err)
	}
	k.status("[задача %s] %s (%s) — эскалация %d/%d: продолжаем на большой модели с разбором причины",
		t.TaskID, diag.event, truncateText(diag.detail, 160), escalation, budget)
	return true, nil
}

// escalateLoop реагирует на зацикливание специалиста (Ф-6, этапы 4.4–4.5).
func (k *KanbanRunner) escalateLoop(ctx context.Context, t *board.Task, reason string) (bool, error) {
	return k.escalate(ctx, t, escalationDiag{
		event:  "зациклилась",
		detail: reason,
		action: "Не повторяй те же действия: перечитай задачу, измени подход и проверь результат.",
	})
}

// forceMajeure — форсмажор фазы (4.5): «Помощь человека» + запись причины.
// В отличие от эскалации петли (escalateLoop) здесь нет задачи для повторного
// прогона: работа архитектора не состоялась, и человек должен решить, что
// делать. Если taskID задан — комментарий с причиной крепится к задаче и её
// статус уходит в human_help; без taskID (фаза архитектора работает до
// появления задач — комментарий требует TaskID) помечается мета-задача
// доски. Ошибки записи не маскируют исходный сбой: только логируются.
//
// human_help не терминален: ensureMeta/touchMeta снимут пометку при следующем
// запуске, а ручной кнопкой доска возвращается в работу.
func (k *KanbanRunner) forceMajeure(ctx context.Context, taskID, reason string) {
	msg := fmt.Sprintf("форс-мажор: %s", truncateText(reason, 400))
	k.setStopReason(msg)

	if taskID != "" {
		if _, err := k.store.AddTaskComment(ctx, taskID, board.Comment{
			Author: "system",
			Type:   board.CommentTypeSystem,
			Body:   "Форсмажор (непредвиденный случай): " + reason + ". Нужно решение человека: разбери причину (логи проекта, вывод проверок) и верни задачу в работу.",
		}); err != nil {
			k.log.Warnf("[доска] форс-мажор задачи %s: комментарий: %v", taskID, err)
		}
		_ = k.store.PatchTask(ctx, taskID, func(cur *board.Task) error {
			if cur.PauseReason == "" {
				cur.PauseReason = truncateText(reason, 800)
			}
			return nil
		})
		if err := k.store.SetTaskStatus(ctx, taskID, board.StatusHumanHelp); err != nil {
			k.log.Warnf("[доска] форс-мажор задачи %s: статус human_help: %v", taskID, err)
		}
	} else if meta, err := k.store.GetMeta(ctx); err == nil && meta != nil && !meta.Status.Terminal() {
		meta.Status = board.StatusHumanHelp
		if err := k.store.SaveMeta(ctx, meta); err != nil {
			k.log.Warnf("[доска] форс-мажор: статус meta human_help: %v", err)
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

// leadFail — 5.4: падение лид-фазы (декомпозиции эпика) останавливает ЭПИК
// с human_help вместо бесконечных повторов цикла: эпик не разобран, повтор с
// тем же промптом ничего не меняет. В отличие от forceMajeure (задача/мета)
// здесь комментарий к эпику пока не ставим — у эпиков на доске нет комментариев
// (только AddTaskComment для задач); причину несут стоп-причина запуска и
// статус эпика, а подробный разбор отложен в Этап 9. SetEpicStatus сам каскадом
// ставит на паузу незакрытые задачи эпика (pauseEpicTasks). Отмена контекста —
// не форсмажор: пробрасываем как есть. Возвращает err без изменений —
// можно писать `return k.leadFail(ctx, epic, err)`.
func (k *KanbanRunner) leadFail(ctx context.Context, epic *board.Epic, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return err
	}
	reason := fmt.Sprintf("декомпозиция эпика %s не удалась: %s", epic.TaskID, truncateText(err.Error(), 300))
	k.setStopReason(reason)
	if serr := k.store.SetEpicStatus(ctx, epic.TaskID, board.StatusHumanHelp); serr != nil {
		k.log.Warnf("[эпик %s] не удалось перевести в human_help: %v", epic.TaskID, serr)
	}
	k.status("[эпик %s] %s — нужна помощь человека", epic.TaskID, reason)
	return err
}
