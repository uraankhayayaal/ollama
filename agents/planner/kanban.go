package planner

import (
	"ai/agents/architect"
	"ai/board"
	"ai/logging"
	"ai/models"
	"ai/tokens"
	"ai/tools"
	"context"
	"errors"
	"fmt"
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
