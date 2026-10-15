package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"ai/domain"
	"ai/domain/services"
	"ai/logging"
)

var (
	defaultSnapshotFactory  services.SnapshotFactory
	defaultPublicAPIAuditor services.PublicAPIAuditor
)

func SetSnapshotFactory(f services.SnapshotFactory) {
	defaultSnapshotFactory = f
}

func SetPublicAPIAuditor(a services.PublicAPIAuditor) {
	defaultPublicAPIAuditor = a
}

// toDomainTaskSpecs переводит декомпозицию лида (domain.TaskSpec) в доменный
// domain.TaskSpec, который хранится в Step.Tasks и сериализуется в
// чекпоинт/PLAN.md. С унификацией моделей (срез C6) domain.TaskSpec —
// алиас domain.TaskSpec, поэтому перевод тождественен.
func toDomainTaskSpecs(tasks []domain.TaskSpec) []domain.TaskSpec {
	return tasks
}

// Executor выполняет план поэтапно, создавая нужных агентов.
// Каждый шаг запускается отдельным агентом с чистым контекстом:
// история не накапливается между шагами, что снижает расход токенов.
//
// Шаги группируются в волны параллельности (ComputeWaves): внутри волны шаги
// независимы, волны идут последовательно. Сейчас шаги волны выполняются
// по одному, но структура готова к параллельному запуску (задел на будущее).
//
// При подключённом чекпоинте (SetCheckpoint) состояние выполнения после
// каждого шага сохраняется в Redis, что позволяет возобновить работу
// с места остановки (resume).
type Executor struct {
	provider LLMProvider
	plan     *Plan
	// completed — шаги, успешно завершённые в текущем запуске (в т.ч.
	// восстановленные из чекпоинта при resume).
	completed map[string]bool
	statuses  map[string]string
	// acceptReports — отчёты приёмки по шагам acceptor (ID шага → отчёт).
	// Заполняются runAcceptorAgent и читаются циклом приёмки при создании
	// плана исправлений.
	acceptReports map[string]*domain.Report
	// acceptor — порт детерминированной приёмки (acceptor.PlannerAdapter).
	// nil — приёмка по шагам qa отключена.
	acceptor Acceptor
	// store — хранилище чекпоинтов (порт); nil — контрольные точки отключены.
	store CheckpointStore
	// resume — возобновлять ли выполнение с чекпоинта.
	resume bool
	// metrics — приёмник именованных метрик шага в единый реестр телеметрии
	// (Ф-1 PLAN-2026-09-19-todo-owerview-for-prom.md). nil — метрики пишутся
	// только в чекпоинт, как раньше (degrade, как у RAG/LSP).
	metrics MetricSink
	// mu сериализует разделяемое состояние исполнителя (completed/statuses,
	// acceptReports, операции с чекпоинтом Store и запись PLAN.md). Нужен,
	// чтобы при ПАРАЛЛЕЛЬНОМ выполнении шагов волны (runWave) обновления
	// Redis-снимка не терялись на read-modify-write и не возникало гонок
	// на картах.
	mu sync.Mutex
	// log — лог проекта (logs/<проект>.log): в режиме serve один процесс ведёт
	// несколько проектов, поэтому сообщения исполнителя пишутся в файл своего
	// проекта, а не в общий файл процесса.
	log *logging.Logger
	rag RAGSearcher

	snapshotFactory services.SnapshotFactory
	apiAuditor      services.PublicAPIAuditor
}

// NewExecutor создаёт исполнителя плана.
func NewExecutor(provider LLMProvider, plan *Plan) *Executor {
	return &Executor{
		provider:        provider,
		plan:            plan,
		completed:       make(map[string]bool),
		statuses:        make(map[string]string),
		acceptReports:   make(map[string]*domain.Report),
		log:             logging.For(planProject(plan)),
		snapshotFactory: defaultSnapshotFactory,
		apiAuditor:      defaultPublicAPIAuditor,
	}
}

// planProject возвращает имя проекта плана; для nil-плана — пустую строку,
// и тогда logging.For направляет сообщения в файл по умолчанию (а не в
// logs/unnamed.log).
func planProject(plan *Plan) string {
	if plan == nil {
		return ""
	}
	return plan.ProjectName
}

// SetCheckpoint подключает хранилище чекпоинтов и включает/выключает режим
// resume. Возвращает сам исполнитель для цепочек вызовов.
func (e *Executor) SetCheckpoint(store CheckpointStore, resume bool) *Executor {
	e.store = store
	e.resume = resume
	return e
}

// CheckpointStore — порт хранилища чекпоинтов плана (реализуется
// checkpoint.Store поверх Redis; инъекция из main). Снимок и статусы —
// доменные типы (domain.CheckpointSnapshot), чтобы application не зависел от
// инфраструктурного пакета checkpoint.
type CheckpointStore interface {
	Save(ctx context.Context, snap *domain.CheckpointSnapshot) error
	Load(ctx context.Context) (*domain.CheckpointSnapshot, error)
	MarkStep(ctx context.Context, snap *domain.CheckpointSnapshot, stepID, status string) error
	SaveRoundState(ctx context.Context, snap *domain.CheckpointSnapshot, stepID string, rounds int, conversation json.RawMessage) error
	ClearRoundState(ctx context.Context, snap *domain.CheckpointSnapshot, stepID string) error
	TrackMetric(ctx context.Context, snap *domain.CheckpointSnapshot, stepID string, values map[string]any) error
}

// MetricSink — приёмник именованных метрик шага в единый реестр телеметрии
// (runmetrics.Registry). Интерфейс, а не конкретный тип, чтобы исполнитель не
// зависел от пакета телеметрии.
type MetricSink interface {
	// SetStepMetric записывает метрику шага. Повторная запись того же ключа
	// перезаписывает значение, а не накапливает его.
	SetStepMetric(scope, step, key string, value float64)
}

// SetMetrics подключает единый реестр телеметрии (Ф-1). Метрики шагов, которые
// раньше оседали только в чекпоинте консольной оркестрации, начинают попадать
// в общий реестр и становятся видны в /metrics и дашборде. nil — отключено.
// Возвращает сам исполнитель для цепочек вызовов.
func (e *Executor) SetMetrics(sink MetricSink) *Executor {
	e.metrics = sink
	return e
}

// SetAcceptor подключает порт детерминированной приёмки (acceptor).
// nil — приёмка по шагам qa отключена.
func (e *Executor) SetAcceptor(a Acceptor) *Executor {
	e.acceptor = a
	return e
}

// SetRAG подключает клиент векторной памяти для контекста RAG по шагам (Ф-4):
// промпт кодирующего шага обогащается блоком «релевантный код по шагу»
// (семантический поиск по тексту шага с фильтром его scope). nil — выключено.
// Возвращает сам исполнитель для цепочек вызовов.
func (e *Executor) SetRAG(r RAGSearcher) *Executor {
	e.rag = r
	return e
}

func (e *Executor) SetSnapshotFactory(f services.SnapshotFactory) *Executor {
	e.snapshotFactory = f
	return e
}

func (e *Executor) SetPublicAPIAuditor(a services.PublicAPIAuditor) *Executor {
	e.apiAuditor = a
	return e
}

// Run выполняет все шаги плана по волнам параллельности с учётом
// зависимостей. При resume пропускает уже завершённые шаги.
func (e *Executor) Run(ctx context.Context) error {
	waves, err := e.plan.ComputeWaves()
	if err != nil {
		return fmt.Errorf("в плане нарушен порядок шагов: %w", err)
	}

	// Инициализация чекпоинта: либо восстанавливаем состояние (resume),
	// либо начинаем с чистого листа.
	if e.store != nil {
		if err := e.initCheckpoint(ctx, waves); err != nil {
			return err
		}
	}

	// PLAN.md: пишем начальный план работ (без декомпозиций — они появятся
	// после шагов лидов), чтобы документ существовал с самого начала.
	if err := e.writePlan(e.plan.ProjectName); err != nil {
		e.log.Warnf("[Plan] не удалось записать PLAN.md: %v", err)
	}

	for _, wave := range waves {
		// Волны остаются строго последовательными; внутри волны шаги
		// независимы и (при CODEGEN_PARALLEL) выполняются одновременно.
		if err := e.runWave(ctx, wave); err != nil {
			return err
		}
	}

	// Цикл приёмки: после выполнения всех шагов плана принимаем собранное
	// приложение. Если приёмка выявила ошибки — планировщик составляет шаги
	// исправления, они выполняются, и приёмка повторяется (бюджет ACCEPT_MAX_ROUNDS).
	if err := e.runAcceptanceLoop(ctx); err != nil {
		return err
	}

	// Финальная запись PLAN.md после полного выполнения (включая шаги
	// исправлений цикла приёмки).
	if err := e.writePlan(e.plan.ProjectName); err != nil {
		e.log.Warnf("[Plan] не удалось записать PLAN.md (финал): %v", err)
	}
	return nil
}

// parallelEnabled — включено ли параллельное выполнение шагов внутри волны.
// По умолчанию ВКЛЮЧЕНО; отключается переменной окружения CODEGEN_PARALLEL
// (значения "0"/"false"/"off"/"no" — последовательный режим).
func parallelEnabled() bool {
	if v := strings.TrimSpace(os.Getenv("CODEGEN_PARALLEL")); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "off", "no":
			return false
		}
	}
	return true
}

// runWave выполняет все шаги одной волны. Волны строго последовательны;
// при параллельном режиме (CODEGEN_PARALLEL) шаги волны стартуют одновременно
// в отдельных горутинах. Первая ошибка (по детерминированному порядку шагов)
// отменяет остальные шаги волны через общий context.
func (e *Executor) runWave(ctx context.Context, wave []string) error {
	if len(wave) == 1 || !parallelEnabled() {
		for _, stepID := range wave {
			if err := e.runOneStep(ctx, stepID); err != nil {
				return err
			}
		}
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for _, stepID := range wave {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			err := e.runOneStep(runCtx, id)
			mu.Lock()
			if err != nil && first == nil {
				first = fmt.Errorf("шаг %q: %w", id, err)
				cancel()
			}
			mu.Unlock()
		}(stepID)
	}
	wg.Wait()

	// Отменённые соседи из-за чужой ошибки возвращают "контекст отменён" —
	// в диагностику уходит только первопричина (первая ошибка по порядку шагов).
	if first != nil {
		return first
	}
	return nil
}

// runOneStep выполняет один шаг плана: resume-пропуск, markRunning, executeStep,
// markDone/markFailed. В параллельном режиме вызывается в отдельной горутине;
// разделяемое состояние и чекпоинт защищены e.mu.
func (e *Executor) runOneStep(ctx context.Context, stepID string) error {
	step := e.findStep(stepID)
	if step == nil {
		return fmt.Errorf("шаг %q не найден в плане", stepID)
	}

	// Resume: шаг уже завершён в прошлом запуске — пропускаем.
	e.mu.Lock()
	done := e.completed[stepID]
	e.mu.Unlock()
	if done {
		e.log.Detailf("[%s] шаг %s уже выполнен ранее, пропускаю (resume)", agentLabel(step.Agent, step.Role), stepID)
		return nil
	}

	e.log.Infof("[%s] шаг %s: %s", agentLabel(step.Agent, step.Role), step.ID, step.Description)
	e.markRunning(ctx, stepID)
	if err := e.executeStep(ctx, step); err != nil {
		e.markFailed(ctx, stepID)
		return fmt.Errorf("шаг %q: %w", step.ID, err)
	}
	e.markDone(ctx, stepID)
	e.log.Infof("[%s] шаг %s завершён", agentLabel(step.Agent, step.Role), step.ID)
	return nil
}

// initCheckpoint восстанавливает состояние при resume или создаёт новый
// снапшот и сохраняет его в Redis.
func (e *Executor) initCheckpoint(ctx context.Context, waves [][]string) error {
	if e.resume {
		if snap, err := e.store.Load(ctx); err == nil && snap != nil {
			// Восстанавливаем завершённые шаги из чекпоинта.
			for id := range snap.Completed {
				e.completed[id] = true
				e.statuses[id] = domain.CheckpointDone
			}
			n := len(snap.Completed)
			e.log.Infof("[Checkpoint] resume: восстановлено %d завершённых шагов", n)
			return nil
		} else if err != nil && err != domain.ErrCheckpointNotFound {
			e.log.Warnf("[Checkpoint] не удалось прочитать чекпоинт (%v), начинаю заново", err)
		}
	}

	// Свежий старт: сохраняем стартовый снапшот.
	snap := e.newSnapshot(waves)
	if err := e.store.Save(ctx, snap); err != nil {
		return fmt.Errorf("сохранение стартового чекпоинта: %w", err)
	}
	return nil
}

// newSnapshot собирает стартовое состояние выполнения плана.
func (e *Executor) newSnapshot(waves [][]string) *domain.CheckpointSnapshot {
	planJSON, _ := json.Marshal(e.plan)
	statuses := make(map[string]string, len(e.plan.Steps))
	for _, s := range e.plan.Steps {
		statuses[s.ID] = domain.CheckpointPending
	}
	return &domain.CheckpointSnapshot{
		ProjectName: e.plan.ProjectName,
		Summary:     e.plan.Summary,
		PlanJSON:    planJSON,
		Completed:   map[string]bool{},
		Statuses:    statuses,
		Waves:       waves,
	}
}

// markRunning помечает шаг как выполняющийся и сбрасывает в чекпоинт.
func (e *Executor) markRunning(ctx context.Context, stepID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.statuses[stepID] = domain.CheckpointRunning
	e.persistStatus(ctx, stepID, domain.CheckpointRunning)
}

// markDone помечает шаг завершённым и сбрасывает в чекпоинт.
func (e *Executor) markDone(ctx context.Context, stepID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.completed[stepID] = true
	e.statuses[stepID] = domain.CheckpointDone
	e.persistStatus(ctx, stepID, domain.CheckpointDone)
	// Шаг успешно завершён — история агентского цикла для resume больше не
	// нужна, удаляем её, чтобы не занимала место в чекпоинте.
	if e.store != nil {
		if snap, err := e.store.Load(ctx); err == nil {
			if len(snap.Conversations) > 0 {
				if _, ok := snap.Conversations[stepID]; ok {
					if err := e.store.ClearRoundState(ctx, snap, stepID); err != nil {
						e.log.Detailf("[Checkpoint] ошибка очистки истории шага %s: %v", stepID, err)
					}
				}
			}
		}
	}
}

// markFailed помечает шаг упавшим и сбрасывает в чекпоинт.
func (e *Executor) markFailed(ctx context.Context, stepID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.statuses[stepID] = domain.CheckpointFailed
	if e.store != nil {
		// Загружаем свежий снапшот и обновляем статус.
		if snap, err := e.store.Load(ctx); err == nil {
			_ = e.store.MarkStep(ctx, snap, stepID, domain.CheckpointFailed)
		}
	}
}

// persistStatus сохраняет статус шага в Redis (если чекпоинт подключён).
// Должен вызываться ТОЛЬКО под защитой e.mu: Store — read-modify-write,
// параллельные вызовы без блокировки теряли бы обновления снапшота.
func (e *Executor) persistStatus(ctx context.Context, stepID, status string) {
	if e.store == nil {
		return
	}
	if snap, err := e.store.Load(ctx); err == nil {
		if err := e.store.MarkStep(ctx, snap, stepID, status); err != nil {
			e.log.Detailf("[Checkpoint] ошибка сохранения статуса %s=%s: %v", stepID, status, err)
		}
	} else {
		e.log.Detailf("[Checkpoint] ошибка чтения снапшота для %s: %v", stepID, err)
	}
}
