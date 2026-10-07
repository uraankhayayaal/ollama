package planner

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/agents/backendlead"
	"ai/agents/developer"
	"ai/agents/devops"
	"ai/agents/devopslead"
	"ai/agents/frontendlead"
	"ai/agents/qaengineer"
	"ai/agents/qalead"
	"ai/board"
	"ai/logging"
	"ai/models"
	"ai/tokens"
	"ai/tools"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxKanbanRounds — максимум Kanban-циклов за один запуск. Цикл выполняет один
// этап: архитектура → декомпозиция лидом → раздача задач специалистам →
// завершение эпиков. Пока задача не решена (все эпики и задачи = done),
// циклы повторяются. Лимит защищает от зацикливания при отменённых записях.
const maxKanbanRounds = 100

// KanbanRunner реализует организационный паттерн «Kanban / Shared Task Board»:
// задача пользователя → эпики (Системный архитектор) → декомпозиция лидами →
// выполнение специалистами → все эпики и задачи выполнены (AllDone) = задача
// решена. Общая доска (эпики, задачи, статусы) хранится в Redis.
//
// Статусы эпиков и задач единые: новая -> в анализе -> готова к работе ->
// в работе -> выполнена (или отменена). Переходы валидирует board.ValidateTransition.
//
// Лиды направлений (frontendlead/backendlead/devopslead/qalead) НЕ пишут код и
// НЕ запускают консольные команды: они исследуют существующий код (List/ReadFiles),
// декомпозируют эпик на задачи для подчинённых (publication через общую доску
// BoardCreateTask/BoardUpdateTask/BoardDeleteTask) и ревизуют задачи при
// изменении содержимого эпика.
type KanbanRunner struct {
	provider models.LLMProvider
	store    *board.Store
	gate     HumanGate
	// boardOnly — режим запуска по доске (кнопка «Продолжить»): новая работа
	// НЕ придумывается (фаза архитектора пропускается — новых эпиков нет), в
	// работу берутся только записи, которые уже есть на доске. Эпики без задач
	// декомпозируются лидами в задачи и исполняются.
	boardOnly bool
	// onStandby — нотификатор режима ожидания: true — работы на доске нет
	// (ожидание), false — работа появилась (цикл возобновлён). Устанавливается
	// сервером для трансляции статуса сессии; nil в консольном режиме.
	onStandby func(bool)
	// outputDir — переключатель рабочей директории специалиста (Ф-3): функция
	// (project, taskID) → каталог работы. Для git-проектов это постоянный
	// worktree ветки задачи (создаётся серверным хуком на статусе in_progress),
	// и правки агента падают в ВЕТКУ ЗАДАЧИ — авто-коммит на done соберёт именно
	// их. nil — общая проектная копия temp/<проект>.
	outputDir func(project, taskID string) string
	// epicOutputDir — резолвер worktree ветки эпика (4.1 архитектор, 5.2 лиды):
	// (project, epicID) → каталог worktree `ai/epic/<id>` либо "" (нельзя
	// создать). Задаётся сервером (SetEpicOutputDir); nil — структуру
	// эпик-ветки архитектор не пишет, лиды работают в temp/<проект>
	// (консольный режим/тесты).
	epicOutputDir func(project, epicID string) string
	// wake — канал событийного побуждения из standby (см. Wake): буфер 1
	// поглощает сигналы, когда runner не ждёт работу.
	wake chan struct{}
	// log — лог проекта (logs/<проект>.log). Устанавливается в Run, когда имя
	// проекта известно; до этого nil, и сообщения идут в файл по умолчанию
	// (нулевой получатель logging.Logger допустим — проверки не нужны).
	log *logging.Logger
	// rag — клиент векторной памяти для фаз архитектора (Ф-1/Ф-4): CodeSearch/
	// RagIndexStatus и блок «релевантный код» в промпте. nil — архитектор
	// работает без RAG (degrade, как раньше).
	rag tools.RAGSearcher
	// architectExtras — серверные мосты-инструменты для архитектора (Ф-4/Р-5):
	// например AskUser для уточняющего вопроса до публикации бэклога.
	// nil/пустой — автономный режим (консоль), архитектор работает без них.
	architectExtras []tools.Tool
	// tokens — счётчик токенов проекта (Ф-2/Ф-3): по scope единицы работы
	// оркестратор снимает накопленный расход и записывает факт в доску при
	// фиксации итогов задачи/эпика. nil — учёта нет (консольный режим):
	// вызовы Generate идут без атрибуции, счётчики не трогаются.
	tokens *tokens.Store
	// onStatus — аудит решений оркестратора в чат проекта (Ф-6, этап 4.6):
	// эскалация модели, исчерпание бюджета автономности, возврат зависших
	// задач. Человек должен видеть, почему система что-то сделала сама.
	// nil — консольный режим, пишем только в лог.
	onStatus func(string)
	// stopReason — причина, по которой работа остановлена и нужен человек
	// (исчерпан бюджет автономии после зацикливания). Хранится отдельно от
	// ошибок запуска: цикл без прогресса сам по себе не объясняет человеку,
	// ЧТО случилось, и без этого поля он увидел бы «нет прогресса».
	stopReason string
	stopMu     sync.Mutex
	// active — задачи, которые ЭТОТ раннер сейчас выполняет. Между фазами их
	// быть не может, поэтому «в работе» вне этого множества — брошенная
	// задача (павший запуск, рестарт сервера, чужой процесс). Это точнее
	// угадывания по времени: чужой живой прогон виден по heartbeat (Ф-6, 4.1).
	active   map[string]bool
	activeMu sync.Mutex
}

// SetStatusNotifier подключает аудит решений оркестратора (эскалация модели,
// возврат зависшей задачи). Сервер транслирует вызовы в чат проекта.
func (k *KanbanRunner) SetStatusNotifier(fn func(string)) *KanbanRunner {
	k.onStatus = fn
	return k
}

// status пишет строку в лог проекта и, если подключён, в чат (Ф-6, 4.6).
func (k *KanbanRunner) status(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if k.log != nil {
		k.log.Infof("%s", msg)
	}
	if k.onStatus != nil {
		k.onStatus(msg)
	}
}

// markActive помечает задачу выполняемой этим раннером (окно, в котором её
// in_progress не считается брошенной).
func (k *KanbanRunner) markActive(taskID string) {
	k.activeMu.Lock()
	defer k.activeMu.Unlock()
	if k.active == nil {
		k.active = map[string]bool{}
	}
	k.active[taskID] = true
}

func (k *KanbanRunner) unmarkActive(taskID string) {
	k.activeMu.Lock()
	defer k.activeMu.Unlock()
	delete(k.active, taskID)
}

// setStopReason запоминает причину остановки работы по вине человека.
func (k *KanbanRunner) setStopReason(msg string) {
	k.stopMu.Lock()
	defer k.stopMu.Unlock()
	if k.stopReason == "" {
		k.stopReason = msg
	}
}

func (k *KanbanRunner) needHuman() string {
	k.stopMu.Lock()
	defer k.stopMu.Unlock()
	return k.stopReason
}

func (k *KanbanRunner) isActive(taskID string) bool {
	k.activeMu.Lock()
	defer k.activeMu.Unlock()
	return k.active[taskID]
}

// standbyPoll — период опроса доски в режиме ожидания: раз в 5 с runner
// проверяет, не появились ли эпики/задачи, которые можно взять в работу.
const standbyPoll = 5 * time.Second

// GateDecision — решение человека по HITL-затвору.
type GateDecision struct {
	// Approved — подтверждено. false означает «переделать»: публикация
	// отменяется, доска очищается, фаза запускается заново.
	Approved bool
	// Reason — комментарий человека (причина отклонения / правки).
	Reason string
}

// HumanGate — интерфейс подтверждения человеком. Устанавливается сервером
// Web UI; в консольном режиме (nil) KanbanRunner работает полностью
// автономно, как раньше. Затворы БЛОКИРУЮТ runner до решения человека —
// пока канал ожидания открыт, пользователь правит доску (редактирование,
// статусы, удаление), а runner при продолжении перечитает её заново.
type HumanGate interface {
	// Epics — подтверждение эпиков, только что опубликованных Системным
	// архитектором, перед декомпозицией лидами.
	Epics(ctx context.Context, epics []*board.Epic) (GateDecision, error)
	// Tasks — подтверждение задач, готовых к работе, перед раздачей
	// специалистам.
	Tasks(ctx context.Context, tasks []*board.Task) (GateDecision, error)
}

// SetGate устанавливает HITL-затвор. nil возвращает автономный режим.
func (k *KanbanRunner) SetGate(g HumanGate) { k.gate = g }

// SetBoardOnly включает/выключает режим «только доска»: новые эпики не
// создаются, а берётся в работу то, что уже есть на доске (эпики без задач
// декомпозируются лидами); при отсутствии работы — режим ожидания (см.
// SetStandbyNotifier).
func (k *KanbanRunner) SetBoardOnly(v bool) { k.boardOnly = v }

// SetStandbyNotifier задаёт нотификатор режима ожидания: fn(true) — работы на
// доске нет (ожидание), fn(false) — работа появилась (цикл возобновлён).
func (k *KanbanRunner) SetStandbyNotifier(fn func(bool)) { k.onStandby = fn }

// SetOutputDir задаёт функцию выбора рабочей директории специалиста по
// (project, taskID) (Ф-3): worktree ветки задачи для git, temp/<проект> —
// стандартно. nil возвращает поведение по умолчанию.
func (k *KanbanRunner) SetOutputDir(fn func(project, taskID string) string) { k.outputDir = fn }

// SetEpicOutputDir задаёт функцию рабочей директории ветки эпика (4.1 архитектор,
// 5.2 лиды): (project, epicID) → worktree ветки `ai/epic/<id>` для git-проектов,
// "" — если worktree создать нельзя (не-git проект, ветки нет). nil (консоль/тесты)
// — структура эпика архитектором не пишется (degrade), лиды пишут скелетон в
// temp/<проект>.
func (k *KanbanRunner) SetEpicOutputDir(fn func(project, epicID string) string) {
	k.epicOutputDir = fn
}

// SetTokens подключает счётчик токенов проекта (Ф-2/Ф-3): раунды Generate
// начинают атрибутироваться единицей работы (scope), а по завершении задачи или
// эпика накопленный расход записывается фактом в доску. nil возвращает
// автономный режим без учёта (консоль).
func (k *KanbanRunner) SetTokens(t *tokens.Store) *KanbanRunner {
	k.tokens = t
	return k
}

// SetRAG подключает клиент векторной памяти к фазам архитектора (Ф-1/Ф-6):
// CodeSearch/RagIndexStatus и блок «релевантный код» в промпте начинают
// работать по индексу. nil — архитектор работает без RAG (degrade). Клиент
// ленивый и nil-safe: недоступный Qdrant/эмбеддинги не роняют фазу.
func (k *KanbanRunner) SetRAG(r tools.RAGSearcher) *KanbanRunner {
	k.rag = r
	return k
}

// SetArchitectExtras дополняет набор инструментов архитектора мостами вне
// общего реестра (Ф-4/Р-5) — например серверным AskUser для уточняющего
// вопроса до публикации бэклога. nil/пустой список возвращает автономный
// режим (консоль). Применяется в фазе архитектора, ревизии черновиков и
// экспертизы багрепортов.
func (k *KanbanRunner) SetArchitectExtras(extra ...tools.Tool) *KanbanRunner {
	k.architectExtras = append([]tools.Tool{}, extra...)
	return k
}

// prepareArchitect применяет к архитектору RAG и серверные extras (Ф-4):
// CodeSearch/RagIndexStatus начинают искать по индексу, мост AskUser
// добавляется в набор инструментов. Повторный вызов безопасен (Set.Add не
// дублирует уже присутствующие имена, SetRAG пересобирает набор из реестра).
func (k *KanbanRunner) prepareArchitect(a *architect.Architect) *architect.Architect {
	if k.rag != nil {
		a.SetRAG(k.rag)
	}
	if len(k.architectExtras) > 0 {
		a.Tools.Add(k.architectExtras...)
	}
	return a
}

// Wake побуждает runner, ожидающий работу на доске (standby), немедленно
// перепроверить её (Ф-2, PLAN-done-dashboard-events) — вместо ожидания следующего
// 5-секундного тика опроса. Сервер зовёт его по событию board_changed из
// другого процесса-компонента (REST-правка доски, созданный чатом эпик).
// Безопасен из любых горутин; вне ожидания — no-op (буфер 1 поглощает сигнал).
func (k *KanbanRunner) Wake() {
	select {
	case k.wake <- struct{}{}:
	default:
	}
}

// NewKanbanRunner создаёт Kanban-оркестратор поверх хранилища доски.
func NewKanbanRunner(provider models.LLMProvider, store *board.Store) *KanbanRunner {
	return &KanbanRunner{provider: provider, store: store, wake: make(chan struct{}, 1)}
}

// waitEpics — HITL-затвор «утвердить эпики»: блокирует до решения человека.
// При отклонении эпики (и их задачи) удаляются — следующая итерация
// перезапустит архитектора.
func (k *KanbanRunner) waitEpics(ctx context.Context) error {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return err
	}
	if len(epics) == 0 {
		return nil
	}
	dec, err := k.gate.Epics(ctx, epics)
	if err != nil {
		return fmt.Errorf("затвор «утвердить эпики»: %w", err)
	}
	if dec.Approved {
		k.log.Infof("[HITL] эпики утверждены (%d): %s", len(epics), epicList(epics))
		return nil
	}
	k.log.Infof("[HITL] эпики отклонены (%s), переделываем", truncateText(dec.Reason, 120))
	return k.rework(ctx, epics)
}

// waitReadyTasks — HITL-затвор «утвердить задачи»: блокирует до решения
// человека. При отклонении задачи не исполняются: эпики, в которых они
// живут, удаляются (каскадом), и весь путь повторяется.
func (k *KanbanRunner) waitReadyTasks(ctx context.Context) error {
	ready, err := k.readyTasks(ctx)
	if err != nil {
		return err
	}
	if len(ready) == 0 {
		return nil
	}
	dec, err := k.gate.Tasks(ctx, ready)
	if err != nil {
		return fmt.Errorf("затвор «утвердить задачи»: %w", err)
	}
	if dec.Approved {
		k.log.Infof("[HITL] задачи утверждены (%d)", len(ready))
		return nil
	}
	k.log.Infof("[HITL] задачи отклонены (%s), переделываем эпики", truncateText(dec.Reason, 120))
	epicIDs := map[string]struct{}{}
	for _, t := range ready {
		epicIDs[t.EpicID] = struct{}{}
	}
	var epics []*board.Epic
	for id := range epicIDs {
		e, err := k.store.GetEpic(ctx, id)
		if err != nil {
			if errors.Is(err, board.ErrNotFound) {
				continue
			}
			return err
		}
		epics = append(epics, e)
	}
	return k.rework(ctx, epics)
}

// rework удаляет эпики вместе с их задачами (каскадом). Эпики, чьи задачи
// уже взяты в работу, удалить нельзя — вернётся первая такая ошибка, чтобы
// человек вмешался вручную.
func (k *KanbanRunner) rework(ctx context.Context, epics []*board.Epic) error {
	var firstErr error
	deleted := 0
	for _, e := range epics {
		if err := k.store.DeleteEpic(ctx, e.TaskID); err != nil {
			if errors.Is(err, board.ErrNotFound) {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("переделка: эпик %s: %w", e.TaskID, err)
			}
			continue
		}
		deleted++
	}
	k.log.Infof("[HITL] переделка: удалено эпиков: %d, проблем: %v", deleted, firstErr)
	return firstErr
}

// Run исполняет Kanban-оркестрацию до решения задачи пользователя.
// В board-only режиме (SetBoardOnly) новые эпики не создаются: цикл берёт в
// работу только записи, которые уже лежат на доске (эпики без задач
// декомпозируются лидами), а при отсутствии работы переходит в режим ожидания
// (см. runBoardOnly).
func (k *KanbanRunner) Run(ctx context.Context, projectName, taskText string) error {
	// Все сообщения ранера — в лог своего проекта: в режиме serve один процесс
	// ведёт несколько проектов, и общий файл смешал бы их строки.
	k.log = logging.For(projectName)

	if k.boardOnly {
		// Запуск по доске: новых записей не создаём (в т.ч. meta), существующую
		// meta-задачу держим «в работе».
		if err := k.touchMeta(ctx); err != nil {
			return err
		}
	} else if err := k.ensureMeta(ctx, projectName, taskText); err != nil {
		return err
	}

	// Задачи, зависшие «в работе» после остановленной/прерванной сессии,
	// возвращаем в «готова к работе» (Ф-6, этап 4.1–4.2): по heartbeat, а не
	// «сбросить всё подряд» — чужой живой прогон трогать нельзя.
	if n, err := k.recoverStuckTasks(ctx); err != nil {
		return err
	} else if n > 0 {
		k.status("[Kanban] зависших «в работе» задач возвращено в очередь: %d", n)
	}

	if k.boardOnly {
		return k.runBoardOnly(ctx)
	}

	for round := 1; round <= maxKanbanRounds; round++ {
		// Задача решена: все эпики и все задачи успешно выполнены.
		done, err := k.finishIfAllDone(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}

		// Watchdog и в начале КАЖДОГО раунда: задача, оставшаяся «в работе»
		// после упавшей фазы (агент/провайдер упал), иначе осталась бы висеть
		// навсегда и уронила бы цикл с «нет прогресса». Между раундами ничего
		// не выполняется, поэтому своих in_progress здесь не бывает.
		if n, err := k.recoverStuckTasks(ctx); err != nil {
			return err
		} else if n > 0 {
			k.status("[Kanban] зависших «в работе» задач возвращено в очередь: %d", n)
		}

		progress, err := k.runPhases(ctx)
		if err != nil {
			return err
		}

		// Ни один этап цикла не сделал работу: на доске остались только
		// отменённые записи или неразрешимые зависимости — дальше бессмысленно.
		if !progress {
			// Если работу остановили мы сами (бюджет автономии), сообщаем
			// ЭТУ причину: «нет прогресса» звучало бы как сбой системы, а
			// человек должен знать, что задача ждёт его решения.
			if stop := k.needHuman(); stop != "" {
				return fmt.Errorf("%s", stop)
			}
			return fmt.Errorf("Kanban-цикл %d: нет прогресса (остались отменённые/заблокированные эпики и задачи), доска: %s",
				round, k.boardSummary(ctx))
		}
	}

	if stop := k.needHuman(); stop != "" {
		return fmt.Errorf("%s", stop)
	}
	return fmt.Errorf("исчерпан бюджет Kanban-раундов (%d), задача не решена", maxKanbanRounds)
}

// runBoardOnly — цикл запуска по доске (кнопка «Продолжить»): новых эпиков не
// создаётся (архитектор пропускается), но существующие эпики декомпозируются
// лидами, а задачи исполняются; когда брать в работу нечего — режим ожидания
// до появления работы на доске. Бюджет раундов ограничивает продуктивную
// работу, но не ожидание: после простоя счётчик обнуляется, поэтому «ждать
// работу» можно бесконечно, а «крутиться без результата» — нет.
func (k *KanbanRunner) runBoardOnly(ctx context.Context) error {
	round := 0
	for {
		round++
		if round > maxKanbanRounds {
			return fmt.Errorf("исчерпан бюджет Kanban-раундов (%d), работа по доске не завершена, доска: %s",
				maxKanbanRounds, k.boardSummary(ctx))
		}
		// Watchdog по heartbeat (Ф-6, 4.1): после «в режиме ожидания» на доске
		// могли остаться задачи «в работе» — их тоже нужно вернуть в очередь.
		if n, err := k.recoverStuckTasks(ctx); err != nil {
			return err
		} else if n > 0 {
			k.status("[Kanban] зависших «в работе» задач возвращено в очередь: %d", n)
		}
		progress, err := k.runPhases(ctx)
		if err != nil {
			return err
		}
		if progress {
			continue
		}

		k.log.Infof("[Kanban] нет эпиков/задач, которые можно взять в работу — режим ожидания (доска: %s)",
			k.boardSummary(ctx))
		k.setStandby(true)
		if err := k.waitForWork(ctx); err != nil {
			return err
		}
		k.setStandby(false)
		k.log.Infof("[Kanban] на доске появилась работа — продолжаю")
		round = 0
	}
}

// runPhases исполняет один Kanban-цикл и сообщает, была ли сделана работа.
// В board-only режиме пропускается только фаза архитектора (новые эпики не
// создаются); эпики, уже лежащие на доске, декомпозируются лидами в задачи и
// исполняются — иначе эпик без задач никогда бы не был взят в работу.
func (k *KanbanRunner) runPhases(ctx context.Context) (bool, error) {
	progress := false

	if !k.boardOnly {
		// Архитектура: эпики -> затвор HITL «утвердить эпики» (перед
		// декомпозицией лидами). Затвор срабатывает только в раунде, где
		// архитектор что-то опубликовал (phaseArchitect вернул true) — в
		// последующих раундах эпики уже есть, и повторного подтверждения
		// не требуется.
		p, err := k.phaseArchitect(ctx)
		if err != nil {
			return false, err
		}
		progress = progress || p
		if k.gate != nil && p {
			if err := k.waitEpics(ctx); err != nil {
				return false, err
			}
		}
	}

	// Ревизия эпиков-черновиков (Ф-8): эпики с RequiresReview=true проверяются
	// Системным архитектором в режиме ревизии ПЕРЕД декомпозицией лидами.
	// Работает в обоих режимах (обычном и board-only): чат-ассистент создаёт
	// черновики на доске в любом из них. Фаза снимает флаг RequiresReview —
	// и только после этого лид может декомпозировать эпик.
	p, err := k.phaseArchitectReview(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p

	// Декомпозиция лидами: в board-only обрабатываются эпики, уже лежащие на
	// доске (созданные чатом/вручную, но ещё без задач), а также их ревизия.
	p, err = k.phaseLeads(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p

	// Готовность к работе -> затвор HITL «утвердить задачи» (перед
	// раздачей специалистам). Срабатывает в раунде, где появились новые
	// готовые задачи; уже подтверждённые потоки в следующих раундах
	// исполняются без повторного подтверждения.
	p, err = k.phaseReady(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p
	if k.gate != nil && p {
		if err := k.waitReadyTasks(ctx); err != nil {
			return false, err
		}
	}

	// Исполнение специалистами.
	p, err = k.phaseExecute(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p

	// Фаза «На тестирование»: задачи в testing доводит тестировщик/специалист.
	p, err = k.phaseTesting(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p

	// Багрепорты и финализация эпиков.
	p, err = k.phaseBugs(ctx)
	if err != nil {
		return false, err
	}
	progress = progress || p

	p, err = k.phaseComplete(ctx)
	if err != nil {
		return false, err
	}

	// Ф-3: проход по доске, фиксирующий факт расхода токенов по всем
	// терминальным единицам. Основные точки фиксации — конец phaseExecute и
	// phaseComplete; проход закрывает переходы, сделанные помимо оркестратора
	// (специалист, чат-ассистент, человек в Web UI). Идемпотентен, ошибки учёта
	// не роняют цикл.
	k.finalizeTokens(ctx)

	return progress || p, nil
}

// finishIfAllDone завершает оркестрацию, если все эпики и задачи выполнены:
// meta-задача проекта переводится в «выполнена». Возвращает true, когда задача
// пользователя решена.
func (k *KanbanRunner) finishIfAllDone(ctx context.Context) (bool, error) {
	done, err := k.store.AllDone(ctx)
	if err != nil {
		return false, err
	}
	if !done {
		return false, nil
	}
	if meta, gerr := k.store.GetMeta(ctx); gerr == nil && meta != nil {
		meta.Status = board.StatusDone
		_ = k.store.SaveMeta(ctx, meta)
	}
	k.log.Infof("[доска] задача пользователя решена: все эпики и задачи выполнены")
	return true, nil
}

// waitForWork опрашивает доску в режиме ожидания: возврат, как только на ней
// появляется работа, которую можно взять (или отменён контекст — остановка).
// Опрос раз в standbyPoll дополнен событийным Wake: сервер будит runner по
// board_changed, так что новая работа подхватывается сразу, а не с задержкой.
func (k *KanbanRunner) waitForWork(ctx context.Context) error {
	ticker := time.NewTicker(standbyPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-k.wake:
			ok, err := k.hasWork(ctx)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
		case <-ticker.C:
			ok, err := k.hasWork(ctx)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
		}
	}
}

// hasWork сообщает, есть ли на доске что взять в работу БЕЗ создания новых
// записей: готовая/работающая задача, задача, которую phaseReady способен
// продвинуть (зависимости и фаза-гейт пройдены), эпик, которому нужна
// декомпозиция/ревизия лида или который готов к финализации, либо багрепорт,
// требующий триажа/экспертизы.
func (k *KanbanRunner) hasWork(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		// «Помощь человека» и «на тестирование» работой не считаются:
		// human_help ждёт ручного выхода, testing возьмёт phaseTesting
		// (этап 7 плана) — пока фазы нет, такие статусы не должны ни
		// будить runner, ни давать «прогресс» циклу (иначе — кручение
		// без результата).
		switch t.Status {
		case board.StatusReady, board.StatusInProgress:
			ok, err := k.epicWorkable(ctx, t.EpicID)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		case board.StatusNew, board.StatusAnalysis:
			ok, err := k.epicWorkable(ctx, t.EpicID)
			if err != nil {
				return false, err
			}
			if !ok {
				continue
			}
			ok, err = k.depsDone(ctx, t)
			if err != nil {
				return false, err
			}
			if !ok {
				continue
			}
			ok, err = k.phasePrereqSatisfied(ctx, t)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
	}

	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	for _, e := range epics {
		if e.Status.Terminal() || e.Status == board.StatusHumanHelp {
			continue
		}
		// Эпик без задач (или с необработанной ревизией) — работа для лида:
		// в board-only он декомпозируется, после чего задачи пойдут в исполнение.
		if len(e.Tasks) == 0 || e.Revision > e.LeadSyncedRev {
			return true, nil
		}
		ok, err := k.epicTasksDone(ctx, e)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}

	bugs, err := k.store.ListBugReports(ctx)
	if err != nil {
		return false, err
	}
	for _, b := range bugs {
		if b.Status == board.BugStatusNew || b.Status == board.BugStatusConfirmed {
			return true, nil
		}
	}
	return false, nil
}

// epicWorkable сообщает, можно ли брать в работу задачи эпика: эпик существует
// и не находится на «помощи человека»/в отмене/выполнен. Остановленные и
// отменённые эпики «замораживают» свои задачи — оркестратор их не трогает
// (код/ветки остаются на месте, позже можно возобновить). Отсутствующий эпик —
// «нет работы» (задача-сирота без родителя исполняться не должна).
func (k *KanbanRunner) epicWorkable(ctx context.Context, epicID string) (bool, error) {
	if epicID == "" {
		return false, nil
	}
	e, err := k.store.GetEpic(ctx, epicID)
	if err != nil {
		if errors.Is(err, board.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	switch e.Status {
	case board.StatusHumanHelp, board.StatusCancelled, board.StatusDone:
		return false, nil
	}
	return true, nil
}

// setStandby сообщает серверу о входе (true) / выходе (false) из режима
// ожидания. Нотификатор может быть не установлен (консольный режим, тесты).
func (k *KanbanRunner) setStandby(v bool) {
	if k.onStandby != nil {
		k.onStandby(v)
	}
}

// ensureMeta создаёт метаданные доски (исходная задача пользователя), если их
// ещё нет, и переводит статус решения в «в работе».
func (k *KanbanRunner) ensureMeta(ctx context.Context, projectName, taskText string) error {
	meta, err := k.store.GetMeta(ctx)
	if errors.Is(err, board.ErrNotFound) {
		meta = &board.Meta{ProjectName: projectName, Task: taskText, Status: board.StatusNew}
		if err := k.store.SaveMeta(ctx, meta); err != nil {
			return err
		}
		k.log.Infof("[доска] проект %q создан: %s", projectName, truncateText(taskText, 80))
	}
	if meta.Status != board.StatusDone {
		meta.Status = board.StatusInProgress
		return k.store.SaveMeta(ctx, meta)
	}
	return nil
}

// touchMeta — meta-задача для board-only режима: существующая запись
// переводится в «в работе», новая НЕ создаётся (запуск по кнопке работает
// только с тем, что уже лежит на доске).
func (k *KanbanRunner) touchMeta(ctx context.Context) error {
	meta, err := k.store.GetMeta(ctx)
	if err != nil {
		if errors.Is(err, board.ErrNotFound) {
			return nil
		}
		return err
	}
	if meta == nil || meta.Status.Terminal() || meta.Status == board.StatusInProgress {
		return nil
	}
	meta.Status = board.StatusInProgress
	if err := k.store.SaveMeta(ctx, meta); err != nil {
		return err
	}
	k.log.Infof("[доска] meta-задача проекта %q в работе", k.store.Project())
	return nil
}

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

	arch := architect.NewArchitectWithStore(k.store.Project(), meta.Task, k.store)
	arch = k.prepareArchitect(arch)
	// 4.1: резолвер worktree ветки эпика. Вызывается архитектором ВНУТРИ
	// submitBacklog — после публикации бэклога, когда эпики уже на доске и их
	// ветки созданы хуком EpicCreatedHook. Основной (первый по порядку) эпик
	// получает структуру проекта; без worktree (консоль/не-git) архитектор
	// получит note и структуру не напишет.
	arch.SetEpicWorkdir(func() (string, string) {
		if k.epicOutputDir == nil {
			return "", ""
		}
		eps, err := k.store.ListEpics(ctx)
		if err != nil || len(eps) == 0 {
			return "", ""
		}
		primary := eps[0]
		for _, e := range eps[1:] {
			if epicLess(e, primary) {
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
	var drafts []*board.Epic
	for _, epic := range epics {
		// Черновики не трогаем только в терминальных/остановленных
		// состояниях («помощь человека» замораживает ревизию, Ф-8): в
		// остальных состояниях ревизия обязательна, пока флаг не снят.
		if !epic.RequiresReview || epic.Status.Terminal() || epic.Status == board.StatusHumanHelp {
			continue
		}
		drafts = append(drafts, epic)
	}
	if len(drafts) == 0 {
		return false, nil
	}

	reviewer := architect.NewArchitectWithStore(k.store.Project(), k.epicReviewPrompt(k.store.Project(), drafts), k.store).AsReviewer()
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
func (k *KanbanRunner) epicReviewPrompt(project string, drafts []*board.Epic) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Проведи ревизию %d эпиков-черновиков на доске проекта %q. Это черновики, созданные чат-ассистентом или вручную: каждому требуется твоя экспертиза перед декомпозицией лидами.\n\n", len(drafts), project)
	for _, epic := range drafts {
		fmt.Fprintf(&b, "- %s «%s» (статус %s, ревизия %d): %s\n",
			epic.TaskID, truncateText(epic.Title, 80), epic.Status.Label(), epic.Revision, truncateText(epic.Description, 400))
	}
	b.WriteString("\nДля каждого эпика: проверь корректность ТЗ, соответствие фактическому стеку проекта (DetectStack), глубину декомпозиции и затронутые смежные модули; скорректируй эпик инструментами доски (BoardUpdateEpic и др.).")
	return b.String()
}

// phaseLeads: декомпозиция и ревизия эпиков лидами направлений. Работа идёт
// по очереди (сериализация): лид декомпозирует следующий эпик только после
// завершения текущей волны задач — лиды не проектируют всё подряд на пустой
// проект. Порядок очереди: инфраструктура (DevOps) -> приложение
// (backend/frontend) -> тестирование (QA Last); внутри фазы — sequence_order,
// затем task_id. Лид работает инструментами доски (BoardCreateTask и др.);
// при недоступности доски — JSON-декомпозицией (fallback). После ревизии
// LeadSyncedRev синхронизируется с ревизией эпика (пересмотр задач при
// изменении контрактов архитектором, Revision > LeadSyncedRev).
func (k *KanbanRunner) phaseLeads(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}

	// Пока на доске есть незавершённые задачи (новая / в анализе / готова к
	// работе / в работе), следующий эпик лиду не выдаётся: сначала должен
	// выполниться уже запланированный объём работы. Исключение — взаимная
	// блокировка: задача ждёт готовности ЭПИКА-зависимости, а этот эпик ещё не
	// разобран. Без исключения цикл вставал: DOL-01 ждал `done` эпика ARCH-01,
	// а разобрать ARCH-01 нельзя, пока DOL-01 не выполнена → «Kanban-цикл N: нет
	// прогресса». В этом случае лиду выдаётся именно эпик-зависимость
	// (см. blockingDependencyEpic), порядок «по одному эпику за раз» сохраняется.
	idle, err := k.pipelineIdle(ctx)
	if err != nil {
		return false, err
	}

	var epic *board.Epic
	var needDecompose, needResync bool
	if idle {
		// Следующий эпик очереди лидов, которому нужна декомпозиция/ревизия.
		epic, needDecompose, needResync, err = k.nextLeadEpic(epics)
		if err != nil {
			return false, err
		}
	} else {
		epic, err = k.blockingDependencyEpic(ctx, epics)
		if err != nil {
			return false, err
		}
		if epic != nil {
			needDecompose, needResync = true, false
			k.log.Infof("[Kanban] на доске есть незавершённые задачи, но они ждут эпик %s "+
				"(зависимость ещё не разобрана) — разбираю его, чтобы снять блокировку", epic.TaskID)
		}
	}
	if epic == nil {
		return false, nil
	}

	lead, err := k.leadFor(epic)
	if err != nil {
		return false, k.leadFail(ctx, epic, fmt.Errorf("фаза лидов, эпик %s: %w", epic.TaskID, err))
	}
	if sb, ok := lead.(interface{ SetBoardStore(*board.Store) }); ok {
		sb.SetBoardStore(k.store)
	}
	// Декомпозиция/ревизия всегда требует публикации задач на доске:
	// лид обязан создать/обновить/удалить задачи для подчинённых.
	if tp, ok := lead.(interface{ SetTaskPublishing(bool) }); ok {
		tp.SetTaskPublishing(true)
	}
	// 5.2: резолвер worktree ветки эпика — лид пишет скелетон в ветку своего
	// эпика, а не в общий temp/<проект>. Резолвер лениво пересоздаёт worktree
	// (мог быть удалён drop-хуком после прошлой фазы), поэтому вызывается на
	// каждой итерации. Без worktree (консоль/не-git) резолвер вернёт "" — лид
	// работает в обычной директории проекта и скелетон не пишет (см. промпт).
	if k.epicOutputDir != nil {
		if sd, ok := lead.(interface{ SetOutputDir(string) }); ok {
			if dir := k.epicOutputDir(k.store.Project(), epic.TaskID); dir != "" {
				sd.SetOutputDir(dir)
			}
		}
	}

	// «В анализе»: для новой декомпозиции обязателен; при ревизии эпик может
	// находиться дальше по цепочке — некритично (переход в анализ не требуется).
	if err := k.store.SetEpicStatus(ctx, epic.TaskID, board.StatusAnalysis); err != nil {
		if _, ok := err.(*board.StatusError); !ok {
			return false, k.leadFail(ctx, epic, fmt.Errorf("эпик %s: перевод в «в анализе»: %w", epic.TaskID, err))
		}
	}
	k.log.Infof("[%s] декомпозиция/ревизия эпика %s (%s) (нужна ревизия: %v)",
		leadName(epic), epic.TaskID, truncateText(epic.Title, 60), needResync)

	resp, err := k.generate(ctx, tokens.ScopeEpic(epic.TaskID), lead)
	if err != nil {
		return false, k.leadFail(ctx, epic, fmt.Errorf("декомпозиция эпика %s: %w", epic.TaskID, err))
	}
	if stop := resp.StopReason(); stop != "" {
		// Ни лимит раундов, ни зацикливание лида не должны ронять весь запуск,
		// если он уже опубликовал на доске частичную (но рабочую) декомпозицию:
		// продолжаем, задачи передадутся специалистам после затвора. Задач нет —
		// эпик пуст, работа не выполнена: эпик уходит в human_help (5.4).
		tasks, terr := k.store.TasksByEpic(ctx, epic.TaskID)
		if terr != nil {
			return false, fmt.Errorf("декомпозиция эпика %s: цикл прерван (%s): %w", epic.TaskID, stop, terr)
		}
		if len(tasks) == 0 {
			return false, k.leadFail(ctx, epic, fmt.Errorf("декомпозиция эпика %s: цикл прерван (%s), задачи не созданы", epic.TaskID, stop))
		}
		k.log.Infof("[эпик %s] цикл лида прерван (%s), но опубликовано задач: %d — продолжаем с частичной декомпозицией", epic.TaskID, stop, len(tasks))
	}

	// Инструментный путь: лид мог создать/изменить задачи прямо на доске
	// (BoardCreateTask/BoardUpdateTask/BoardDeleteTask). Задачи уже в эпике.
	if !needDecompose {
		// Ревизия: доводим до «готова к работе», задачи мог обновить лид.
		// LeadSyncedRev синхронизируем ниже.
	} else {
		// Падение на JSON-декомпозицию, если лид не создал задачи
		// инструментами доски (fallback для тестов и standalone-режима).
		tasks, err := k.store.TasksByEpic(ctx, epic.TaskID)
		if err != nil {
			return false, err
		}
		if len(tasks) == 0 {
			dec, err := board.UnmarshalTasks(resp.Content)
			if err != nil {
				return false, k.leadFail(ctx, epic, fmt.Errorf("декомпозиция эпика %s: лид не создал задач ни доской, ни JSON: %w", epic.TaskID, err))
			}
			if len(dec) == 0 {
				return false, k.leadFail(ctx, epic, fmt.Errorf("декомпозиция эпика %s: лид вернул пустой список tasks", epic.TaskID))
			}
			for i := range dec {
				ts := dec[i]
				if err := k.store.CreateTask(ctx, &board.Task{TaskSpec: ts, EpicID: epic.TaskID, Assignee: assignee(ts)}); err != nil {
					return false, fmt.Errorf("эпик %s: создание задачи %s: %w", epic.TaskID, ts.TaskID, err)
				}
			}
			k.log.Infof("[эпик %s] декомпозирован на %d задач (JSON-fallback)", epic.TaskID, len(dec))
		} else {
			k.log.Infof("[эпик %s] декомпозирован на %d задач (инструменты доски)", epic.TaskID, len(tasks))
		}
	}

	// Синхронизация ревизии: с этой ревизии доски лид больше не обязан
	// пересматривать задачи, пока архитектор не изменит эпик снова.
	synced, err := k.store.GetEpic(ctx, epic.TaskID)
	if err != nil {
		return false, fmt.Errorf("эпик %s: чтение после декомпозиции: %w", epic.TaskID, err)
	}
	synced.LeadSyncedRev = synced.Revision
	if err := k.store.SaveEpic(ctx, synced); err != nil {
		return false, fmt.Errorf("эпик %s: сохранение ревизии: %w", epic.TaskID, err)
	}

	if !needDecompose && needResync {
		// Ревизия: статус «в работе» уже не откатываем; доводим эпик к работе,
		// если он стоит «в анализе» или «готова к работе».
	} else if !needResync {
		if err := k.store.SetEpicStatus(ctx, epic.TaskID, board.StatusReady); err != nil {
			if _, ok := err.(*board.StatusError); !ok {
				return false, fmt.Errorf("эпик %s: перевод в «готов к работе»: %w", epic.TaskID, err)
			}
		}
	}
	k.log.Infof("[эпик %s] готов (ревизия %d, синхронизировано лидом %d)", epic.TaskID, synced.Revision, synced.LeadSyncedRev)
	// «Список обновлённых задач»: после декомпозиции/ревизии лида печатаем
	// в консоль актуальный состав задач эпика, чтобы человек видел итог.
	published, listErr := k.store.TasksByEpic(ctx, synced.TaskID)
	if listErr != nil {
		return false, fmt.Errorf("эпик %s: чтение задач после декомпозиции: %w", synced.TaskID, listErr)
	}
	printLeadTasks(k.log, synced, published)
	return true, nil
}

// printLeadTasks выводит в консоль и лог проекта текущие задачи эпика после
// работы лида: ID, статус, исполнитель, заголовок и приоритет/параллельность.
func printLeadTasks(log *logging.Logger, epic *board.Epic, tasks []*board.Task) {
	log.Infof("[Список обновлённых задач] эпик %s (%s) — %d задач:", epic.TaskID, truncateText(epic.Title, 60), len(tasks))
	for _, t := range tasks {
		log.Infof("  - %s [%s] -> %s | %s (порядок %d, параллельно: %v)",
			t.TaskID, t.Status.Label(), t.Assignee, truncateText(t.Title, 60), t.SequenceOrder.Int(), t.CanRunParallel.Bool())
	}
}

// pipelineIdle сообщает, что на доске нет незавершённых задач (все предыдущие
// декомпозиции выполнены, отменены или остановлены). Только в этом состоянии
// лиду выдаётся следующая декомпозиция: сначала выполняется уже запланированный
// объём работы. Задачи на «помощи человека» (эпик остановлен) не блокируют
// очередь — остановка эпика не должна замораживать декомпозицию остальных
// эпиков. «На тестирование» — активная работа: конвейер лидов пока не свободен.
func (k *KanbanRunner) pipelineIdle(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		switch t.Status {
		case board.StatusDone, board.StatusCancelled, board.StatusHumanHelp:
			continue
		default:
			return false, nil
		}
	}
	return true, nil
}

// staleTaskMinDefault — сколько ждать пульса задачи, прежде чем считать прогон
// брошенным. Heartbeat обновляет агент каждый раунд (Ф-6, этап 2), поэтому
// порог — «заметно дольше одного раунда», а не точное время. Перекрывается
// переменной KANBAN_STALE_TASK_MIN; 0 отключает порог (тогда брошенной
// считается любая задача вне active).
const staleTaskMinDefault = 30

func staleTaskAfter() time.Duration {
	if v := strings.TrimSpace(os.Getenv("KANBAN_STALE_TASK_MIN")); v != "" {
		if mins, err := strconv.Atoi(v); err == nil && mins >= 0 {
			return time.Duration(mins) * time.Minute
		}
	}
	return staleTaskMinDefault * time.Minute
}

// recoverStuckTasks возвращает в «готова к работе» задачи, зависшие «в работе».
// Иначе их никто не исполнит (phaseExecute берёт только готовые), а
// незавершённая задача блокирует и pipeline (очередь лидов), и финализацию
// эпиков — цикл падал бы с «нет прогресса».
//
// Решение принимается по двум сигналам, а не «сбросить всё подряд» (Ф-6,
// этап 4.1):
//
//   - задача, которую выполняет ЭТОТ раннер (k.active), жива по определению;
//   - задача со СВЕЖИМ heartbeat принадлежит живому прогону (другой процесс
//     оркестрации, ещё работающий агент) — её не трогаем. Прежний код
//     полагался на «в этом раунде своих in_progress ещё нет», из-за чего два
//     запуска одного проекта мешали друг другу.
//
// Всё остальное — брошено: пульс старше порога либо его нет вовсе (задача
// старше State Tracking или агент не отчитывался). Переход «в работе» →
// «готова к работе» теперь разрешён конечным автоматом (см. board/entity.go),
// поэтому статус идёт через SetTaskStatus, а не в обход валидации.
//
// «Помощь человека» и «на тестирование» сюда НЕ попадают: human_help снимается
// только вручную (иначе авто-цикл заменил бы собой ручной разбор), testing
// ждёт тестировщика.
func (k *KanbanRunner) recoverStuckTasks(ctx context.Context) (int, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return 0, err
	}
	stale := staleTaskAfter()
	reset := 0
	for _, t := range tasks {
		if t.Status != board.StatusInProgress {
			continue
		}
		if k.isActive(t.TaskID) {
			continue
		}
		reason := "пульса не было"
		if hb := strings.TrimSpace(t.HeartbeatAt); hb != "" {
			age, err := time.Parse(time.RFC3339, hb)
			switch {
			case err != nil:
				reason = fmt.Sprintf("нечитаемый пульс %q", hb)
			case stale > 0 && time.Since(age) < stale:
				k.log.Detailf("[задача %s] «в работе», пульс %s назад — не трогаем (живой прогон)",
					t.TaskID, time.Since(age).Truncate(time.Second))
				continue
			default:
				reason = fmt.Sprintf("пульс %s назад", time.Since(age).Truncate(time.Second))
			}
		}
		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusReady); err != nil {
			return 0, fmt.Errorf("задача %s: возврат «в работе» → «готова к работе»: %w", t.TaskID, err)
		}
		k.status("[задача %s] возврат в очередь: была «в работе», но %s", t.TaskID, reason)
		reset++
	}
	return reset, nil
}

// epicPhase классифицирует эпик по фазе разработки (аналог taskPhase для задач):
// инфраструктура (DevOps) -> приложение (backend/frontend) -> тестирование (QA).
func epicPhase(e *board.Epic) string {
	switch {
	case isRole(e.AssignedRole, "qa", "тест", "testing"):
		return "test"
	case isRole(e.AssignedRole, "devops", "инфра", "infra", "sre", "docker", "k8s", "ci"):
		return "infra"
	default:
		return "app"
	}
}

// epicPhaseRank — приоритет фазы эпика в очереди лидов: инфраструктура раньше
// приложения, тестирование (в т.ч. qalead) — после всех задач разработки.
func epicPhaseRank(phase string) int {
	switch phase {
	case "infra":
		return 0
	case "app":
		return 1
	default: // test
		return 2
	}
}

// epicLess — приоритет эпиков в очереди лидов: сначала фаза разработки
// (инфраструктура -> приложение -> тестирование, QA Last), затем
// sequence_order и, для детерминизма, task_id.
func epicLess(a, b *board.Epic) bool {
	ar, br := epicPhaseRank(epicPhase(a)), epicPhaseRank(epicPhase(b))
	if ar != br {
		return ar < br
	}
	ao, bo := a.SequenceOrder.Int(), b.SequenceOrder.Int()
	if ao != bo {
		return ao < bo
	}
	return a.TaskID < b.TaskID
}

// blockingDependencyEpic ищет эпик, который блокирует уже запланированные
// задачи: у незавершённой задачи в dependencies стоит ID эпика, который ещё не
// декомпозирован лидом (len(epic.Tasks)==0), не ждёт ревизии архитектора
// (RequiresReview=false) и не терминален/не остановлен. Такой эпик разбирается
// вне очереди `pipelineIdle` — иначе задача и эпик блокируют друг друга.
//
// Возвращает nil, если блокирующих эпиков нет: тогда поведение phaseLeads
// прежнее (следующий эпик выдаётся только на «пустой» доске).
func (k *KanbanRunner) blockingDependencyEpic(ctx context.Context, epics []*board.Epic) (*board.Epic, error) {
	byID := make(map[string]*board.Epic, len(epics))
	for _, e := range epics {
		byID[e.TaskID] = e
	}
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var best *board.Epic
	for _, t := range tasks {
		switch t.Status {
		case board.StatusDone, board.StatusCancelled, board.StatusHumanHelp:
			continue
		}
		for _, dep := range t.Dependencies {
			depEpic, ok := byID[dep]
			if !ok || depEpic.Status.Terminal() || depEpic.Status == board.StatusHumanHelp {
				continue
			}
			if depEpic.RequiresReview || len(depEpic.Tasks) > 0 {
				continue
			}
			if best == nil || epicLess(depEpic, best) {
				best = depEpic
			}
		}
	}
	return best, nil
}

// nextLeadEpic выбирает следующий эпик для декомпозиции/ревизии лидом по
// приоритету очереди: инфраструктура -> приложение -> тестирование (QA Last),
// внутри фазы — sequence_order, затем task_id. Возвращает (nil, false, false),
// если ни одному эпику декомпозиция/ревизия не требуется.
func (k *KanbanRunner) nextLeadEpic(epics []*board.Epic) (*board.Epic, bool, bool, error) {
	var best *board.Epic
	bestDecompose := false
	for _, epic := range epics {
		// Терминальные и остановленные эпики не трогаем: «помощь человека»
		// замораживает и декомпозицию, и ревизию лидом (код остаётся в
		// своей ветке).
		if epic.Status.Terminal() || epic.Status == board.StatusHumanHelp {
			continue
		}
		// Черновик (requires_review=true, Ф-8) не декомпозируется лидом, пока
		// Системный архитектор не утвердил его ревизией: лид получает эпик
		// только после снятия флага. Иначе черновик чата разнёсся бы на задачи
		// без экспертизы архитектора.
		if epic.RequiresReview {
			continue
		}
		needDecompose := len(epic.Tasks) == 0
		needResync := !needDecompose && epic.Revision > epic.LeadSyncedRev
		if !needDecompose && !needResync {
			continue
		}
		if best == nil || epicLess(epic, best) {
			best = epic
			bestDecompose = needDecompose
		}
	}
	if best == nil {
		return nil, false, false, nil
	}
	return best, bestDecompose, !bestDecompose, nil
}

// phaseReady: задачи, у которых выполнены все зависимости и фаза-гейт
// (инфраструктура → приложение → тестирование), переводятся «новая» ->
// «в анализе» -> «готова к работе» (готова к раздаче специалистам).
func (k *KanbanRunner) phaseReady(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}

	// «Новая»: зависимости и фаза-порядок выполнены — «в анализе».
	progress := false
	for _, t := range tasks {
		if t.Status != board.StatusNew {
			continue
		}
		ok, err := k.epicWorkable(ctx, t.EpicID)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		ok, err = k.depsDone(ctx, t)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		ok, err = k.phasePrereqSatisfied(ctx, t)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusAnalysis); err != nil {
			return false, fmt.Errorf("задача %s: новая -> в анализе: %w", t.TaskID, err)
		}
		k.log.Infof("[задача %s] новая → в анализе", t.TaskID)
		progress = true
	}

	// «В анализе»: фаза-гейт пройден и готова к раздаче — «готова к работе».
	// Перечитаем доску: первый цикл только что перевёл «новые» в «в анализе».
	tasks, err = k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		if t.Status != board.StatusAnalysis {
			continue
		}
		ok, err := k.epicWorkable(ctx, t.EpicID)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		ok, err = k.phasePrereqSatisfied(ctx, t)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusReady); err != nil {
			return false, fmt.Errorf("задача %s: в анализе -> готова к работе: %w", t.TaskID, err)
		}
		k.log.Infof("[задача %s] в анализе → готова к работе", t.TaskID)
		progress = true
	}
	return progress, nil
}

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

// phaseBugs: конвейер багрепортов. QA-специалисты публикуют багрепорты
// (BoardCreateBugReport, статус new) во время выполнения задач. QA Lead
// отсеивает «нейрослоп» (new -> slop) и подтверждает реальные проблемы (new ->
// confirmed). Системный архитектор в режиме экспертизы выносит вердикт по
// подтверждённым багрепортам (confirmed -> fix|feature|wont_fix), при вердикте
// fix создавая эпик исправления.
func (k *KanbanRunner) phaseBugs(ctx context.Context) (bool, error) {
	bugs, err := k.store.ListBugReports(ctx)
	if err != nil {
		return false, err
	}
	progress := false

	var newBugs, confirmedBugs []*board.BugReport
	for _, b := range bugs {
		switch b.Status {
		case board.BugStatusNew:
			newBugs = append(newBugs, b)
		case board.BugStatusConfirmed:
			confirmedBugs = append(confirmedBugs, b)
		}
	}

	// Триаж QA Lead: подтвердить или отсечь «нейрослоп».
	if len(newBugs) > 0 {
		qa := qalead.NewQALead(k.store.Project(), k.bugTriagePrompt(k.store.Project(), newBugs))
		if sb, ok := any(qa).(interface{ SetBoardStore(*board.Store) }); ok {
			sb.SetBoardStore(k.store)
		}
		// Триаж багрепортов — не декомпозиция: QA Lead работает статусами
		// багрепортов (BoardSetBugStatus), публиковать задачи не обязан.
		if tp, ok := any(qa).(interface{ SetTaskPublishing(bool) }); ok {
			tp.SetTaskPublishing(false)
		}
		resp, err := k.generate(ctx, tokens.ScopeBugs, qa)
		if err != nil {
			return false, fmt.Errorf("фаза триажа багрепортов: %w", err)
		}
		if err := resp.LoopError("фаза триажа багрепортов"); err != nil {
			return false, err
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("фаза триажа багрепортов: цикл остановлен по лимиту раундов")
		}
		k.log.Infof("[QA Lead] триаж %d багрепортов", len(newBugs))
		progress = true
	}

	// Экспертиза Системного архитектора: вердикт по подтверждённым багрепортам.
	if len(confirmedBugs) > 0 {
		arch := architect.NewArchitectWithStore(k.store.Project(),
			k.bugExpertPrompt(k.store.Project(), confirmedBugs), k.store).AsBugExpert()
		arch = k.prepareArchitect(arch)
		resp, err := k.generate(ctx, tokens.ScopeBugs, arch)
		if err != nil {
			return false, fmt.Errorf("фаза экспертизы багрепортов: %w", err)
		}
		if err := resp.LoopError("фаза экспертизы багрепортов"); err != nil {
			return false, err
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("фаза экспертизы багрепортов: цикл остановлен по лимиту раундов")
		}
		k.log.Infof("[Системный архитектор] экспертиза %d багрепортов", len(confirmedBugs))
		progress = true
	}

	return progress, nil
}

// phaseComplete: эпик становится «выполнен» только после завершения всех его
// задач (в работе -> выполнена).
func (k *KanbanRunner) phaseComplete(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	progress := false
	for _, epic := range epics {
		if epic.Status == board.StatusDone || epic.Status.Terminal() {
			continue
		}
		// Остановленный эпик не финализируем: «помощь человека» — временное
		// состояние, завершение дождётся выхода (вручную).
		if epic.Status == board.StatusHumanHelp {
			continue
		}
		if len(epic.Tasks) == 0 {
			continue
		}
		allDone, err := k.epicTasksDone(ctx, epic)
		if err != nil {
			return false, err
		}
		if allDone {
			// «Доводим» эпик до «выполнена» из любого не-терминального статуса
			// по цепочке new -> analysis -> ready -> in_progress -> done (из
			// testing цепочка заходит через in_progress; human_help фаза выше
			// пропускает). Новая задача продвигает последующие; статусы
			// менеджер переместил вручную (resume) — по цепочке с текущего
			// места.
			advanced := false
			for _, tgt := range []board.Status{
				board.StatusAnalysis, board.StatusReady,
				board.StatusInProgress, board.StatusDone,
			} {
				if err := k.store.SetEpicStatus(ctx, epic.TaskID, tgt); err == nil {
					advanced = true
					continue
				} else if _, ok := err.(*board.StatusError); ok {
					continue
				} else {
					return false, fmt.Errorf("эпик %s: продвижение статуса: %w", epic.TaskID, err)
				}
			}
			if advanced {
				k.log.Infof("[эпик %s] выполнен: %s", epic.TaskID, truncateText(epic.Title, 60))
				progress = true
				// Ф-3: факт расхода токенов эпика (задачи + лид + доля
				// архитектора) фиксируется сразу после выполнения.
				if err := k.finalizeEpicTokens(ctx, epic.TaskID); err != nil {
					k.log.Warnf("[учёт токенов] эпик %s: %v", epic.TaskID, err)
				}
				// Багрепорты, направленные на эпик исправления, закрываются
				// (fix -> fixed): исправление поставлено и проверено.
				if n, err := k.store.MarkBugsFixedForEpic(ctx, epic.TaskID); err != nil {
					return false, fmt.Errorf("эпик %s: закрытие багрепортов: %w", epic.TaskID, err)
				} else if n > 0 {
					k.log.Infof("[эпик %s] закрыты багрепорты: %d", epic.TaskID, n)
					progress = true
				}
			}
		}
	}
	return progress, nil
}

// epicTasksDone сообщает, что все задачи эпика выполнены (и их хотя бы одна):
// признак того, что эпик можно финализировать.
func (k *KanbanRunner) epicTasksDone(ctx context.Context, epic *board.Epic) (bool, error) {
	if len(epic.Tasks) == 0 {
		return false, nil
	}
	for _, taskID := range epic.Tasks {
		t, err := k.store.GetTask(ctx, taskID)
		if err != nil {
			return false, fmt.Errorf("эпик %s: чтение задачи %s: %w", epic.TaskID, taskID, err)
		}
		if t.Status != board.StatusDone {
			return false, nil
		}
	}
	return true, nil
}

// noteEpicProgress переводит эпик в «в работе», если он в статусе
// «готова к работе» (а его задачи начали выполняться).
func (k *KanbanRunner) noteEpicProgress(ctx context.Context, epicID string) {
	epic, err := k.store.GetEpic(ctx, epicID)
	if err != nil || epic.Status != board.StatusReady {
		return
	}
	if err := k.store.SetEpicStatus(ctx, epicID, board.StatusInProgress); err != nil {
		k.log.Detailf("[эпик %s] в работу: %v", epicID, err)
	}
}

// readyTasks возвращает задачи «готова к работе» (эпик не остановлен/не
// отменён), отсортированные по порядку (sequence_order) и ID.
func (k *KanbanRunner) readyTasks(ctx context.Context) ([]*board.Task, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var ready []*board.Task
	for _, t := range tasks {
		if t.Status != board.StatusReady {
			continue
		}
		ok, err := k.epicWorkable(ctx, t.EpicID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ready = append(ready, t)
	}
	sortTasks(ready)
	return ready, nil
}

// taskPhase классифицирует задачу по фазе разработки по её assigned_role:
// инфраструктура (DevOps), тестирование (QA) или приложение (остальное).
// Используется для фаза-гейтинга «инфраструктура → приложение → тестирование».
func taskPhase(t *board.Task) string {
	switch {
	case isRole(t.AssignedRole, "qa", "тест", "testing"):
		return "test"
	case isRole(t.AssignedRole, "devops", "инфра", "infra", "sre", "docker", "k8s", "ci"):
		return "infra"
	default:
		return "app"
	}
}

// phasePrereqSatisfied проверяет фаза-порядок в рамках эпика задачи:
// прикладные задачи ждут завершения инфраструктурных задач эпика, тестовые —
// завершения всех НЕ тестовых задач эпика. Отменённые задачи считаются
// «прошедшими», инфраструктурные задачи гейта не имеют.
func (k *KanbanRunner) phasePrereqSatisfied(ctx context.Context, t *board.Task) (bool, error) {
	phase := taskPhase(t)
	if phase == "infra" {
		return true, nil
	}
	epic, err := k.store.GetEpic(ctx, t.EpicID)
	if err != nil {
		return false, err
	}
	for _, taskID := range epic.Tasks {
		if taskID == t.TaskID {
			continue
		}
		other, err := k.store.GetTask(ctx, taskID)
		if err != nil {
			return false, err
		}
		if other.Status == board.StatusCancelled {
			continue
		}
		otherPhase := taskPhase(other)
		var required bool
		switch phase {
		case "app":
			required = otherPhase == "infra"
		case "test":
			required = otherPhase != "test"
		}
		if required && other.Status != board.StatusDone {
			return false, nil
		}
	}
	return true, nil
}

// depsDone проверяет, что все зависимости задачи (задачи/эпики доски)
// уже выполнены. При несуществующей зависимости возвращает ошибку доски.
func (k *KanbanRunner) depsDone(ctx context.Context, t *board.Task) (bool, error) {
	if len(t.Dependencies) == 0 {
		return true, nil
	}
	for _, dep := range t.Dependencies {
		if task, err := k.store.GetTask(ctx, dep); err == nil {
			if task.Status != board.StatusDone {
				return false, nil
			}
			continue
		} else if !errors.Is(err, board.ErrNotFound) {
			return false, err
		}
		if epic, err := k.store.GetEpic(ctx, dep); err == nil {
			if epic.Status != board.StatusDone {
				return false, nil
			}
			continue
		} else if !errors.Is(err, board.ErrNotFound) {
			return false, err
		}
		return false, fmt.Errorf("задача %s: зависимость %q не существует на доске", t.TaskID, dep)
	}
	return true, nil
}

// boardSummary собирает краткую сводку доски для логов и ошибок. Устойчиво к
// отмене контекста/ошибкам хранилища: если счётчики не прочитались (например,
// контекст отменён прямо во время ожидания работы), сводка показывает нули.
func (k *KanbanRunner) boardSummary(ctx context.Context) string {
	ec, _ := k.store.EpicCounts(ctx)
	tc, _ := k.store.TaskCounts(ctx)
	if ec == nil {
		ec = &board.StatusCounts{By: map[board.Status]int{}}
	}
	if tc == nil {
		tc = &board.StatusCounts{By: map[board.Status]int{}}
	}
	return fmt.Sprintf("эпиков: %d (выполнено %d, отменено %d), задач: %d (выполнено %d, отменено %d)",
		ec.Total, ec.By[board.StatusDone], ec.By[board.StatusCancelled],
		tc.Total, tc.By[board.StatusDone], tc.By[board.StatusCancelled])
}

// leadFor создаёт агента-лида направления по assigned_role эпика и собирает
// промпт декомпозиции. Лиды пишут ТОЛЬКО скелетон (allowlist в инструменте,
// worktree ветки эпика подключается в phaseLeads через epicOutputDir); QA-лид
// пишет readme до Этапа 8.
func (k *KanbanRunner) leadFor(epic *board.Epic) (agents.Agent, error) {
	prompt := k.leadPrompt(epic)
	project := k.store.Project()
	switch {
	case isRole(epic.AssignedRole, "qa", "тест", "testing"):
		return qalead.NewQALead(project, prompt), nil
	case isRole(epic.AssignedRole, "devops", "инфра", "infra", "dev", "sre", "ci"):
		return devopslead.NewDevopsLead(project, prompt), nil
	case isRole(epic.AssignedRole, "front", "react", "ui", "client", "фронт"):
		return frontendlead.NewFrontendLead(project, prompt), nil
	default: // backend
		return backendlead.NewBackendLead(project, prompt), nil
	}
}

// bugTriagePrompt формирует задание QA Lead на триаж багрепортов (new):
// подтвердить (BoardSetBugStatus, confirmed) или отсеять «нейрослоп» (slop).
func (k *KanbanRunner) bugTriagePrompt(project string, bugs []*board.BugReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Проведи триаж %d багрепортов QA-специалистов проекта %q с доски.\n\n", len(bugs), project)
	for _, bug := range bugs {
		fmt.Fprintf(&b, "- %s (статус new): %s\n  Репортёр: %s, задача %s, эпик %s\n  %s\n",
			bug.BugID, bug.Title, bug.ReporterRole, bug.TaskID, bug.EpicID, truncateText(bug.Description, 400))
	}
	b.WriteString("\nДля каждого багрепорта вызови BoardSetBugStatus: реальная, воспроизводимая проблема — status=confirmed; надуманное, «нейрослоп», дубль или жалоба без фактов — status=slop.\n")
	b.WriteString("Ответь текстом, что триаж выполнен.")
	return b.String()
}

// bugExpertPrompt формирует задание архитектора (режим экспертизы) по
// подтверждённым багрепортам: вынести вердикт BoardReviewBugReport
// (fix|feature|wont_fix) и при fix создать эпик исправления.
func (k *KanbanRunner) bugExpertPrompt(project string, bugs []*board.BugReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Системный архитектор проекта %q: QA Lead подтвердил %d багрепортов, требуется твоя экспертиза.\n\n", project, len(bugs))
	for i, bug := range bugs {
		fmt.Fprintf(&b, "%d) %s — %s\n   Репортёр %s, задача %s, эпик %s.\n   %s\n",
			i+1, bug.BugID, bug.Title, bug.ReporterRole, bug.TaskID, bug.EpicID, truncateText(bug.Description, 600))
	}
	b.WriteString("\nДля каждого багрепорта вызови BoardReviewBugReport с вердиктом: fix (создай эпик исправления), feature (это фича), wont_fix (не исправляем). Параметры эпика исправления укажи в том же вызове (epic_task_id, epic_title, epic_description, epic_assigned_role, epic_sequence_order).")
	return b.String()
}

// leadPrompt формирует задание лиду: эпик, архитектурная сводка. Лид публикует
// задачи инструментами доски (если они доступны), иначе — JSON-декомпозицией.
// 5.3: НЕ-QA лиды (backend/frontend/devops) дополнительно пишут СКЕЛЕТОН в
// worktree ветки эпика: контракты живут в коде скелетона, задачи ссылаются на
// файлы, а не дублируют контракт текстом. QA-лид скелетона не пишет (у него
// нет Run и работ с кодом — readme-only до Этапа 8), для него сохраняется
// старый формат «полный контракт в description».
func (k *KanbanRunner) leadPrompt(epic *board.Epic) string {
	// Скелетон пишут все лиды, кроме QA: их направления имеют Write/Run и
	// worktree ветки эпика (см. phaseLeads → SetOutputDir).
	skeleton := !isRole(epic.AssignedRole, "qa", "тест", "testing")
	var b strings.Builder
	b.WriteString("Декомпозируй эпик из бэклога Системного архитектора на задачи для рядовых специалистов.\n\n")
	fmt.Fprintf(&b, "Проект: %s\n", k.store.Project())
	fmt.Fprintf(&b, "Эпик: %s — %s\n", epic.TaskID, epic.Title)
	if epic.Summary != "" {
		fmt.Fprintf(&b, "Архитектурная сводка:\n%s\n\n", epic.Summary)
	}
	fmt.Fprintf(&b, "Описание эпика:\n%s\n\n", epic.Description)
	b.WriteString("Публикация задач:\n")
	if skeleton {
		b.WriteString("- Если у тебя есть инструменты доски (BoardCreateTask и др.) — создавай задачи ими (объём, приёмочные критерии и отсылки к файлам скелетона: путь + символ/строка; assigned_role, sequence_order, dependencies). Обновляй и удаляй задачи через BoardUpdateTask/BoardDeleteTask (кроме взятых в работу).\n")
	} else {
		b.WriteString("- Если у тебя есть инструменты доски (BoardCreateTask и др.) — создавай задачи ими (полный контракт в description, assigned_role, sequence_order, dependencies). Обновляй и удаляй задачи через BoardUpdateTask/BoardDeleteTask (кроме взятых в работу).\n")
	}
	b.WriteString("- Если инструментов доски нет — верни строго JSON-декомпозицию (без markdown-обёрток) по схеме:\n")
	b.WriteString("{\n")
	b.WriteString(`  "lead_summary": "краткое техническое описание модуля",` + "\n")
	b.WriteString(`  "tasks": [` + "\n")
	b.WriteString("    {\n")
	b.WriteString(`      "task_id": "уникальный ID (например T-01)",` + "\n")
	b.WriteString(`      "title": "название задачи",` + "\n")
	if skeleton {
		b.WriteString(`      "description": "объём и приёмочные критерии задачи плюс ОБЯЗАТЕЛЬНЫЕ отсылки к файлам скелетона (путь + имя символа), которые ты спроектировал; полный текст контракта не копируется — он зафиксирован в коде скелетона",` + "\n")
	} else {
		b.WriteString(`      "description": "детальное техническое описание задачи с готовым контрактом взаимодействия, который ты спроектировал",` + "\n")
	}
	b.WriteString(`      "assigned_role": "роль специалиста (например Senior Go Developer / React Developer / QA Engineer / DevOps Engineer)",` + "\n")
	b.WriteString("      \"sequence_order\": 1,\n")
	b.WriteString(`      "can_run_parallel": true,` + "\n")
	b.WriteString(`      "dependencies": [` + "\n")
	b.WriteString("      ]\n")
	b.WriteString("    }\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n\n")
	if skeleton {
		b.WriteString("СКЕЛЕТОН В ВЕТКЕ ЭПИКА (обязательно, если OutputDir — git worktree): до публикации задач спроектируй контракты КОДОМ скелетона в ветке эпика — каркасы файлов своего направления (типы/интерфейсы/заготовки конфигов) с комментариями-подсказками, без реализации. Затем прогони проверку через Run (цель из корневого Makefile), закоммить и запуши (git add → git commit → git push). Если .git нет — скелетон не пиши: только декомпозиция.\n\n")
		b.WriteString("ДЕТАЛИЗАЦИЯ ЗАДАЧ (обязательно для каждой задачи): описание обязано содержать отсылки к коду скелетона — файл и символ (путь + имя интерфейса/функции/сервиса, где возможно — строка из ReadMap), объём и приёмочные критерии. ПОЛНЫЙ текст контракта в description НЕ копируй: специалист читает файл скелетона, а не пересказ. Зафиксируй правило: специалист не меняет публичные сигнатуры/схемы из скелетона.\n\n")
	} else {
		b.WriteString("ДЕТАЛИЗАЦИЯ КОНТРАКТОВ (обязательно для каждой задачи): описание задачи обязано содержать ПОЛНЫЙ контракт, по которому специалист пишет код без догадок: точные типы/структуры (поля с типами), публичные сигнатуры функций/методов/интерфейсов (имя, параметры с типами, возвращаемые значения), API-контракты (метод, путь, схема запроса/ответа, коды ошибок), схему БД, манифесты с конкретными значениями. Зафиксируй контракт в description — специалист не меняет публичные сигнатуры и схемы.\n\n")
	}
	b.WriteString("Правила:\n")
	if skeleton {
		b.WriteString("- Ты пишешь ТОЛЬКО скелетон (в ветке эпика), НЕ реализацию: контракты фиксируй кодом скелетона, задачи — отсылками к этим файлам. Run — только проверка (сборка/тесты/автостиль) и git-пуш скелетона; реализацию и удаление файлов выполняют специалисты по твоим задачам.\n")
	} else {
		b.WriteString("- Ты НЕ пишешь код и НЕ запускаешь команды: только проектируешь контракты и раздаёшь задачи.\n")
	}
	b.WriteString("- Порядок разработки: сначала инфраструктура, затем приложение, затем тестирование — проставляй sequence_order и зависимости так, чтобы этот порядок соблюдался (тестовые задачи зависят от прикладных, прикладные — от инфраструктурных).\n")
	if skeleton {
		b.WriteString("- Единый источник контрактов — код скелетона: задачи ссылаются на файлы, контракт не дублируется в описаниях.\n")
	} else {
		b.WriteString("- Контракты взаимодействия дублируй в описание каждой связанной задачи (единый источник истины).\n")
	}
	b.WriteString("- Чётко проставь sequence_order и dependencies: какие задачи параллельны (can_run_parallel: true), какие блокируют друг друга.\n")
	return b.String()
}

// specialistFor создаёт агента-специалиста для задачи: QA/DevOps или
// разработчик (backend/frontend). Разработчик-агент создаёт недостающую
// структуру подпроекта с нуля и дорабатывает существующий код; выбор
// специализации (frontend/backend) идёт по роли задачи (маркеры фронтенда)
// либо по умолчанию — backend.
func (k *KanbanRunner) specialistFor(t *board.Task) (agents.Agent, error) {
	return SpecialistForRole(k.store.Project(), t.AssignedRole, k.taskPrompt(t)), nil
}

// SpecialistForRole создаёт агента-специалиста по роли задачи: QA/DevOps или
// разработчик (backend/frontend). Общий для Kanban- и plan-исполнителей:
// выбор специализации (QA/DevOps/frontend/backend) идёт по роли задачи.
// Экспортирован для server (авторезолвинг конфликтов мёрджа через разработчика).
func SpecialistForRole(project, role, prompt string) agents.Agent {
	switch {
	case isRole(role, "qa", "тест", "testing"):
		return qaengineer.NewQAEngineer(project, prompt)
	case isRole(role, "devops", "инфра", "infra", "sre", "docker", "k8s", "ci"):
		return devops.NewDevops(project, prompt)
	}

	// Разработка кода: задача с фронтенд-маркерами отдаётся Frontend-агенту,
	// всё остальное (включая смешанные/backend/универсальные задачи) — Backend-
	// агенту. Оба агента работают в temp/<project> в корне модуля и сами
	// создают недостающую структуру подпроекта.
	if isRole(role, "front", "react", "ui", "client", "фронт", "js", "ts", "vue") {
		return developer.NewFrontendDeveloper(project, prompt)
	}
	return developer.NewBackendDeveloper(project, prompt)
}

// taskPrompt формирует задание специалисту по задаче с Kanban-доски.
func (k *KanbanRunner) taskPrompt(t *board.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ты — специалист (%s), выполняешь задачу с общей Kanban-доски проекта %q.\n\n", t.AssignedRole, k.store.Project())
	fmt.Fprintf(&b, "Задача на доске: %s — %s\n\n", t.TaskID, t.Title)
	b.WriteString("Постановка задачи:\n")
	b.WriteString(t.Description)
	b.WriteString("\n\n")
	if len(t.Repositories) > 1 {
		b.WriteString("В задаче участвуют связанные git-репозитории: ")
		b.WriteString(strings.Join(t.Repositories, ", "))
		b.WriteString(". Корень OutputDir содержит их по путям из .gitmodules. Используй пути внутри OutputDir; изменения во вложенных репозиториях будут зафиксированы и опубликованы отдельно.\n\n")
	}
	b.WriteString("Правила:\n")
	b.WriteString("- Работай в своей выходной директории (OutputDir): учи структуру через List, читай контракты через ReadFiles.\n")
	b.WriteString("- Описание задачи — это отсылки к файлам («контракт в ./server/..., см. ./docs/...»), а не полный текст кода или структуры: читай перечисленные файлы из ветки эпика и не вставляй их содержимое в ответы/комментарии.\n")
	b.WriteString("- Выполни задачу, прогони сборку и проверки через Run, доведи до зелёного состояния.\n")
	b.WriteString("- Не выходи за пределы своей части монорепозитория (роль задана промптом).\n")
	fmt.Fprintf(&b, "- Если у тебя есть инструменты доски: идентификатор задачи %s. Ты можешь читать её контракт (BoardGetTask) и комментарии к ней; ОБЯЗАТЕЛЬНО учти описание и комментарии задачи при выполнении. Когда работа полностью выполнена (сборка и проверки зелёные) — ОБЯЗАТЕЛЬНО переведи задачу в статус testing вызовом BoardSetTaskStatus («На тестирование»).\n", t.TaskID)
	b.WriteString("- Если ты QA-инженер и нашёл дефект по контракту — оформи багрепорт инструментом BoardCreateBugReport (статус new), его разберут QA Lead и архитектор.")
	return b.String()
}

// assignee возвращает ключ специалиста для задачи: присваиваем роль из
// assigned_role (одна задача — один специалист).
func assignee(ts board.TaskSpec) string {
	if strings.TrimSpace(ts.AssignedRole) == "" {
		return "developer"
	}
	return ts.AssignedRole
}

// isRole проверяет, содержит ли роль (в нижнем регистре) один из маркеров.
func isRole(role string, markers ...string) bool {
	role = strings.ToLower(role)
	for _, m := range markers {
		if strings.Contains(role, m) {
			return true
		}
	}
	return false
}

// sortTasks сортирует задачи по sequence_order, затем по TaskID (детерминизм).
func sortTasks(tasks []*board.Task) {
	for i := 1; i < len(tasks); i++ {
		for j := i; j > 0; j-- {
			a, b := tasks[j-1], tasks[j]
			if a.SequenceOrder.Int() < b.SequenceOrder.Int() ||
				(a.SequenceOrder.Int() == b.SequenceOrder.Int() && a.TaskID <= b.TaskID) {
				continue
			}
			tasks[j-1], tasks[j] = tasks[j], tasks[j-1]
		}
	}
}

// epicList форматирует список эпиков для логов.
func epicList(epics []*board.Epic) string {
	ids := make([]string, 0, len(epics))
	for _, e := range epics {
		ids = append(ids, e.TaskID+" ("+truncateText(e.Title, 40)+")")
	}
	return strings.Join(ids, ", ")
}

// leadName описывает лида направления для логов.
func leadName(epic *board.Epic) string {
	switch {
	case isRole(epic.AssignedRole, "qa", "тест", "testing"):
		return "QA Lead"
	case isRole(epic.AssignedRole, "devops", "инфра", "infra", "dev", "sre", "ci"):
		return "DevOps Lead"
	case isRole(epic.AssignedRole, "front", "react", "ui", "client", "фронт"):
		return "Frontend Lead"
	default:
		return "Backend Lead"
	}
}

// truncateText обрезает длинный текст для логов.

// phaseTesting доводит задачи в статусе "на тестирование" (StatusTesting)
// до "выполнено". QA (или назначенный специалист) берёт задачу из testing,
// доводит до done, либо возвращает замечаниями в работу/human_help.
func (k *KanbanRunner) phaseTesting(ctx context.Context) (bool, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	progress := false
	busy := map[string]bool{}
	for _, t := range tasks {
		if t.Status != board.StatusTesting {
			continue
		}
		if busy[t.Assignee] {
			continue
		}
		busy[t.Assignee] = true
		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusInProgress); err != nil {
			return false, fmt.Errorf("задача %s: testing -> in_progress: %w", t.TaskID, err)
		}
		k.noteEpicProgress(ctx, t.EpicID)
		specialist, err := k.specialistFor(t)
		if err != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, err)
		}
		if sb, ok := specialist.(interface{ SetBoardStore(*board.Store) }); ok {
			sb.SetBoardStore(k.store)
		}
		if k.outputDir != nil {
			if dir := k.outputDir(k.store.Project(), t.TaskID); dir != "" {
				if so, ok := specialist.(interface{ SetOutputDir(string) }); ok {
					so.SetOutputDir(dir)
				}
			}
		}
		if k.store != nil {
			if sn, ok := specialist.(interface{ SetProjectName(string) }); ok {
				sn.SetProjectName(k.store.Project())
			}
			if ti, ok := specialist.(interface{ SetTaskID(string) }); ok {
				ti.SetTaskID(t.TaskID)
			}
		}
		taskCtx := board.NewInjectionContext(ctx, t.Injections)
		taskCtx = board.NewCommentContext(taskCtx, t.Comments)
		if k.store != nil {
			taskCtx = board.WithInjectionSource(taskCtx, func(ctx context.Context) ([]board.Injection, error) {
				fresh, err := k.store.GetTask(ctx, t.TaskID)
				if err != nil {
					return nil, err
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
		taskCtx = board.WithInjectionScope(taskCtx, board.InjectionScope{Project: k.store.Project(), TaskID: t.TaskID, Role: t.Assignee})
		if t.ModelTier == board.ModelTierLarge {
			taskCtx = models.WithHeavyModel(taskCtx)
		}
		k.markActive(t.TaskID)
		resp, genErr := k.generate(taskCtx, tokens.ScopeTask(t.TaskID), specialist)
		k.unmarkActive(t.TaskID)
		if genErr != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, genErr)
		}
		if resp != nil && resp.Looped {
			done, err := k.escalateLoop(ctx, t, resp.LoopReason)
			if err != nil {
				return false, err
			}
			progress = progress || done
			continue
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("задача %s: цикл остановлен по лимиту раундов", t.TaskID)
		}
		current, err := k.store.GetTask(ctx, t.TaskID)
		if err != nil {
			return false, fmt.Errorf("задача %s: чтение статуса после работы: %w", t.TaskID, err)
		}
		switch current.Status {
		case board.StatusDone:
			if err := k.finalizeTaskTokens(ctx, t.TaskID); err != nil {
				k.log.Warnf("[учёт токенов] задача %s: %v", t.TaskID, err)
			}
			progress = true
			continue
		case board.StatusHumanHelp, board.StatusCancelled, board.StatusTesting:
			progress = true
			continue
		default:
			progress = true
			continue
		}
	}
	return progress, nil
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return strings.TrimSpace(s)
	}
	trimmed := strings.TrimSpace(s)
	if len(trimmed) <= n {
		return trimmed
	}
	return trimmed[:n] + "…"
}
