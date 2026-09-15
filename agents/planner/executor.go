package planner

import (
	"ai/agents"
	"ai/agents/acceptor"
	"ai/agents/backendlead"
	"ai/agents/developer"
	"ai/agents/devops"
	"ai/agents/devopslead"
	"ai/agents/frontendlead"
	"ai/agents/qaengineer"
	"ai/agents/qalead"
	"ai/board"
	"ai/checkpoint"
	"ai/logging"
	"ai/models"
	"ai/projects"
	"ai/runner"
	"ai/tools"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ollama/ollama/api"
)

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
	provider models.LLMProvider
	plan     *Plan
	// completed — шаги, успешно завершённые в текущем запуске (в т.ч.
	// восстановленные из чекпоинта при resume).
	completed map[string]bool
	statuses  map[string]string
	// acceptReports — отчёты приёмки по шагам acceptor (ID шага → отчёт).
	// Заполняются runAcceptorAgent и читаются циклом приёмки при создании
	// плана исправлений.
	acceptReports map[string]*acceptor.Report
	// store — Redis-хранилище чекпоинтов; nil — контрольные точки отключены.
	store *checkpoint.Store
	// resume — возобновлять ли выполнение с чекпоинта.
	resume bool
	// mu сериализует разделяемое состояние исполнителя (completed/statuses,
	// acceptReports, операции с чекпоинтом Store и запись PLAN.md). Нужен,
	// чтобы при ПАРАЛЛЕЛЬНОМ выполнении шагов волны (runWave) обновления
	// Redis-снимка не терялись на read-modify-write и не возникало гонок
	// на картах.
	mu sync.Mutex
}

// NewExecutor создаёт исполнителя плана.
func NewExecutor(provider models.LLMProvider, plan *Plan) *Executor {
	return &Executor{
		provider:      provider,
		plan:          plan,
		completed:     make(map[string]bool),
		statuses:      make(map[string]string),
		acceptReports: make(map[string]*acceptor.Report),
	}
}

// SetCheckpoint подключает Redis-хранилище чекпоинтов и включает/выключает
// режим resume. Возвращает сам исполнитель для цепочек вызовов.
func (e *Executor) SetCheckpoint(store *checkpoint.Store, resume bool) *Executor {
	e.store = store
	e.resume = resume
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
		logging.Warnf("[Plan] не удалось записать PLAN.md: %v", err)
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
		logging.Warnf("[Plan] не удалось записать PLAN.md (финал): %v", err)
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
		logging.Detailf("[%s] шаг %s уже выполнен ранее, пропускаю (resume)", agentLabel(step.Agent, step.Role), stepID)
		return nil
	}

	logging.Infof("[%s] шаг %s: %s", agentLabel(step.Agent, step.Role), step.ID, step.Description)
	e.markRunning(ctx, stepID)
	if err := e.executeStep(ctx, step); err != nil {
		e.markFailed(ctx, stepID)
		return fmt.Errorf("шаг %q: %w", step.ID, err)
	}
	e.markDone(ctx, stepID)
	logging.Infof("[%s] шаг %s завершён", agentLabel(step.Agent, step.Role), step.ID)
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
				e.statuses[id] = checkpoint.StatusDone
			}
			n := len(snap.Completed)
			logging.Infof("[Checkpoint] resume: восстановлено %d завершённых шагов", n)
			return nil
		} else if err != nil && err != checkpoint.ErrNotFound {
			logging.Warnf("[Checkpoint] не удалось прочитать чекпоинт (%v), начинаю заново", err)
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
func (e *Executor) newSnapshot(waves [][]string) *checkpoint.Snapshot {
	planJSON, _ := json.Marshal(e.plan)
	statuses := make(map[string]string, len(e.plan.Steps))
	for _, s := range e.plan.Steps {
		statuses[s.ID] = checkpoint.StatusPending
	}
	return &checkpoint.Snapshot{
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
	e.statuses[stepID] = checkpoint.StatusRunning
	e.persistStatus(ctx, stepID, checkpoint.StatusRunning)
}

// markDone помечает шаг завершённым и сбрасывает в чекпоинт.
func (e *Executor) markDone(ctx context.Context, stepID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.completed[stepID] = true
	e.statuses[stepID] = checkpoint.StatusDone
	e.persistStatus(ctx, stepID, checkpoint.StatusDone)
	// Шаг успешно завершён — история агентского цикла для resume больше не
	// нужна, удаляем её, чтобы не занимала место в чекпоинте.
	if e.store != nil {
		if snap, err := e.store.Load(ctx); err == nil {
			if len(snap.Conversations) > 0 {
				if _, ok := snap.Conversations[stepID]; ok {
					if err := e.store.ClearRoundState(ctx, snap, stepID); err != nil {
						logging.Detailf("[Checkpoint] ошибка очистки истории шага %s: %v", stepID, err)
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
	e.statuses[stepID] = checkpoint.StatusFailed
	if e.store != nil {
		// Загружаем свежий снапшот и обновляем статус.
		if snap, err := e.store.Load(ctx); err == nil {
			_ = e.store.MarkStep(ctx, snap, stepID, checkpoint.StatusFailed)
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
			logging.Detailf("[Checkpoint] ошибка сохранения статуса %s=%s: %v", stepID, status, err)
		}
	} else {
		logging.Detailf("[Checkpoint] ошибка чтения снапшота для %s: %v", stepID, err)
	}
}

// trackStepMetrics обновляет метрики шага в чекпоинте (теляметрия стадий:
// раунды, вердикт приёмки, изменения контракта, изменённые файлы). Работает
// только при подключённом чекпоинте; на ход плана не влияет.
func (e *Executor) trackStepMetrics(ctx context.Context, stepID string, values map[string]any) {
	if e.store == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if snap, err := e.store.Load(ctx); err == nil {
		if err := e.store.TrackMetric(ctx, snap, stepID, values); err != nil {
			logging.Detailf("[Checkpoint] ошибка сохранения метрик шага %s: %v", stepID, err)
		}
	}
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
func (e *Executor) auditContractStep(ctx context.Context, step *Step, projectName string, before *tools.PublicAPISnapshot) {
	after, err := tools.BuildPublicAPISnapshot(projects.ProjectDir(projectName))
	if err != nil {
		logging.Detailf("[%s] шаг %s: не удалось снять повторный снимок API: %v", agentLabel(step.Agent, step.Role), step.ID, err)
		return
	}
	diff := tools.ComparePublicAPI(before, after)
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
		logging.Warnf("[%s] шаг %s: ИЗМЕНЕНИЕ КОНТРАКТА (публичная поверхность): %d деклараций:\n  %s", agentLabel(step.Agent, step.Role), step.ID, len(diff), strings.Join(constrained, "\n  "))
		return
	}
	logging.Detailf("[%s] шаг %s: изменение контракта (публичная поверхность): %d деклараций:\n  %s", agentLabel(step.Agent, step.Role), step.ID, len(diff), strings.Join(constrained, "\n  "))
}

// auditFileChanges выводит дайджест файловых правок шага (что субагент
// создал/изменил/удалил) и сохраняет счётчики в метрики чекпоинта. Ничего не
// откатывает — это аудит, а не контроль: код на всякий случай не трогается.
func (e *Executor) auditFileChanges(ctx context.Context, step *Step, snap *tools.Snap) {
	added, modified, removed, err := snap.Diff()
	if err != nil {
		logging.Detailf("[%s] шаг %s: не удалось вычислить diff правок: %v", agentLabel(step.Agent, step.Role), step.ID, err)
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
	logging.Detailf("[%s] шаг %s: правки субагента: +%d создано, ~%d изменено, -%d удалено", agentLabel(step.Agent, step.Role), step.ID, len(added), len(modified), len(removed))
	if len(added) > 0 {
		logging.Detailf("[%s] шаг %s:   создано: %s", agentLabel(step.Agent, step.Role), step.ID, limit(added))
	}
	if len(modified) > 0 {
		logging.Detailf("[%s] шаг %s:   изменено: %s", agentLabel(step.Agent, step.Role), step.ID, limit(modified))
	}
	if len(removed) > 0 {
		logging.Detailf("[%s] шаг %s:   удалено: %s", agentLabel(step.Agent, step.Role), step.ID, limit(removed))
	}
}

// rollbackStep откатывает директорию проекта к снимку, сделанному перед
// началом выполнения шага: восстанавливает изменённые/удалённые файлы и
// удаляет созданные субагентом. Вызывается ТОЛЬКО при реальной ошибке шага
// (не при исчерпании раундов/обрезке по токенам — там откат поломал бы resume:
// сохранённая история диалога ссылается на уже существующие файлы).
//
// Возвращает true, если откат реально что-то изменил.
func (e *Executor) rollbackStep(projectName, stepID string, snap *tools.Snap) bool {
	if snap == nil || !rollbackEnabled() {
		return false
	}
	restored, removed, err := snap.Restore()
	if err != nil {
		logging.Warnf("[откат] шаг %s: не удалось откатить директорию %s: %v", stepID, projectName, err)
		return false
	}
	if len(restored) == 0 && len(removed) == 0 {
		logging.Detailf("[откат] шаг %s: снимок актуален, правок не потребовалось", stepID)
		return false
	}
	if len(restored) > 0 {
		logging.Warnf("[откат] шаг %s: восстановлено файлов: %d (изменённые/удалённые субагентом): %v", stepID, len(restored), restored)
	}
	if len(removed) > 0 {
		logging.Warnf("[откат] шаг %s: удалено файлов, созданных упавшим субагентом: %d: %v", stepID, len(removed), removed)
	}
	return true
}

// executeStep выполняет один шаг плана нужным агентом.
func (e *Executor) executeStep(ctx context.Context, step *Step) error {
	projectName := step.ProjectName
	if projectName == "" {
		projectName = e.plan.ProjectName
	}

	switch step.Agent {
	case AgentBackendDev, AgentFrontendDev, AgentDevops:
		_, err := e.runCodingAgent(ctx, step, projectName)
		return err

	case AgentDevopsLead, AgentQALead, AgentFrontendLead, AgentBackendLead:
		return e.runLeadStep(ctx, step, projectName)

	case AgentQAEngineer:
		// Объединённый агент QA: сборка → автотесты → приёмка (детерминированная).
		return e.runAcceptorAgent(ctx, step, projectName)

	default:
		return fmt.Errorf("неизвестный тип агента: %s", step.Agent)
	}
}

// newStepAgent создаёт агента шага по его типу (разработчик, специалист или
// лид направления). Возвращает nil для неизвестного типа.
func newStepAgent(step *Step, projectName string) agents.Agent {
	switch step.Agent {
	case AgentBackendDev:
		return developer.NewBackendDeveloper(projectName, step.Prompt)
	case AgentFrontendDev:
		return developer.NewFrontendDeveloper(projectName, step.Prompt)
	case AgentDevops:
		return devops.NewDevops(projectName, step.Prompt)
	case AgentDevopsLead:
		return devopslead.NewDevopsLead(projectName, step.Prompt)
	case AgentQAEngineer:
		return qaengineer.NewQAEngineer(projectName, step.Prompt)
	case AgentQALead:
		return qalead.NewQALead(projectName, step.Prompt)
	case AgentFrontendLead:
		return frontendlead.NewFrontendLead(projectName, step.Prompt)
	case AgentBackendLead:
		return backendlead.NewBackendLead(projectName, step.Prompt)
	}
	return nil
}

// runCodingAgent запускает агента-разработчика (backend/frontend) или другого
// специалиста (devops, qa, лиды). Все агенты ограничены областью работы scope:
// пишут/читают только указанные файлы. Возвращает ответ модели (нужен циклу
// QA-багрепортов: observable из него парсятся репорты дефектов).
func (e *Executor) runCodingAgent(ctx context.Context, step *Step, projectName string) (*runner.AgentResponse, error) {
	agent := newStepAgent(step, projectName)
	if agent == nil {
		return nil, fmt.Errorf("неизвестный тип агента: %s", step.Agent)
	}

	// Ограничиваем инструменты агента областью работы шага: вне scope он не
	// сможет ни читать, ни писать, ни удалять файлы. Для шагов создания scope
	// предварительно расширяется до директорий (см. effectiveCreationScope).
	scope := effectiveCreationScope(projects.ProjectDir(projectName), step.Scope)
	if len(scope) != len(step.Scope) {
		logging.Detailf("[%s] шаг %s: scope шага расширен для создания: %v -> %v", agentLabel(step.Agent, step.Role), step.ID, step.Scope, scope)
	}

	// Снимок проекта перед выполнением шага: при реальной ошибке шага его
	// область откатывается к этому состоянию, чтобы сломанные правки упавшего
	// субагента (в т.ч. удалённый/переписанный им чужой функционал) не утекли
	// в общий репозиторий (см. rollbackEnabled/rollbackStep). Снимок СКОПИРОВАН
	// по области шага (NewSnapScoped): при параллельном выполнении волны откат
	// одного шага не должен трогать файлы соседних. PLAN.md исключается — им
	// управляет исполнитель, и параллельный шаг не удаляет его при откате.
	var snap *tools.Snap
	if rollbackEnabled() {
		var s *tools.Snap
		var serr error
		if len(scope) > 0 {
			s, serr = tools.NewSnapScoped(projects.ProjectDir(projectName), scope, planDocName)
		} else {
			s, serr = tools.NewSnap(projects.ProjectDir(projectName))
		}
		if serr == nil {
			snap = s
			logging.Detailf("[%s] шаг %s: защитный откат активен (снимок: %d файлов, scope %d записей)", agentLabel(step.Agent, step.Role), step.ID, s.FileCount(), len(scope))
		} else {
			logging.Warnf("[%s] шаг %s: не удалось снять снимок для отката: %v", agentLabel(step.Agent, step.Role), step.ID, serr)
		}
	}

	if scoper, ok := agent.(interface{ SetScope([]string) }); ok {
		scoper.SetScope(scope)
	}

	// Роль разработчика (frontend/backend): для backend/frontend-агентов она
	// определяется самим типом агента (роль в шаге хранится для журналирования);
	// если шаг несёт роль generic-агента, способному её применить — передаём
	// её агенту. Ограничивает промпт и стиль работы, но не заменяет scope.
	if rr, ok := agent.(interface{ SetRole(string) }); ok && step.Role != "" {
		rr.SetRole(string(step.Role))
	}

	// Возобновление агентского цикла: если в чекпоинте сохранена история
	// диалога (в прошлом запуске шаг упёрся в лимит раундов), передаём её
	// в цикл, чтобы продолжить с места остановки, а не начинать заново.
	genCtx := ctx
	if rs, ok := e.loadResumeState(ctx, step.ID); ok {
		logging.Detailf("[%s] шаг %s: возобновляю агентский цикл с раунда %d (повторный запуск с --resume)", agentLabel(step.Agent, step.Role), step.ID, rs.Rounds+1)
		genCtx = runner.WithResumeState(ctx, rs)
	}

	// Снимок публичной поверхности (нормализованные сигнатуры пакетов) до шага.
	// После выполнения diff покажет, как изменился контракт — добавление/
	// удаление/изменение публичных деклараций. Не ошибка плана, а сигнал для
	// человека (см. auditContractStep); при CODEGEN_CONTRACT_ENFORCE=1 уровень
	// лога поднимается до предупреждения.
	var apiBefore *tools.PublicAPISnapshot
	if contractAuditEnabled() {
		if s, serr := tools.BuildPublicAPISnapshot(projects.ProjectDir(projectName)); serr == nil {
			apiBefore = s
		} else {
			logging.Detailf("[%s] шаг %s: не удалось снять снимок API: %v", agentLabel(step.Agent, step.Role), step.ID, serr)
		}
	}

	resp, err := e.provider.Generate(genCtx, agent)
	if err != nil {
		// Реальная ошибка выполнения шага (провайдер/инструменты): откатываем
		// проект к состоянию на момент старта шага. На ошибках «исчерпан лимит
		// раундов» (resp.Truncated) откат не выполняется — resume продолжит
		// существующий диалог и файлы.
		e.rollbackStep(projectName, step.ID, snap)
		return nil, err
	}

	// Аудит изменения контракта: сравниваем поверхность до/после шага.
	if apiBefore != nil {
		e.auditContractStep(ctx, step, projectName, apiBefore)
	}

	// Аудит файловых правок шага: какие файлы субагент создал/изменил/удалил.
	// Диагностический дайджест для человека и метрика стадии для чекпоинта
	// (не ошибка плана, план правят приёмка и анализатор).
	if snap != nil {
		e.auditFileChanges(ctx, step, snap)
	}

	// Цикл упёрся в лимит раундов (или модель обрезалась по лимиту токенов):
	// сохраняем историю диалога в чекпоинт, чтобы следующий запуск с --resume
	// продолжил шаг с раунда resp.Rounds+1, и останавливаем выполнение плана.
	if resp != nil && resp.Truncated {
		if e.persistRoundState(ctx, step, resp) {
			logging.Warnf("[%s] шаг %s: истощён лимит раундов (%d), история сохранена в чекпоинт", agentLabel(step.Agent, step.Role), step.ID, resp.Rounds)
		}
		if strings.TrimSpace(resp.Content) == "" {
			// Модель исчерпала лимит и вернула пустой ответ без вызовов
			// инструментов — код не создан. Считаем шаг упавшим, чтобы
			// чекпоинт пометил его failed и можно было повторить через resume.
			return nil, fmt.Errorf("агент %T не создал код: модель вернула пустой ответ (исчерпан лимит раундов: %d)", agent, resp.Rounds)
		}
		if e.store != nil {
			// Чекпоинт подключён — останавливаем план: пользователь запустит
			// следующий запуск с --resume, и шаг продолжится с раунда
			// resp.Rounds+1 (например 13..24, затем снова resume — 25..36).
			return nil, fmt.Errorf("шаг %q: исчерпан лимит раундов (%d) агентского цикла, история сохранена — запустите с --resume, чтобы продолжить", step.ID, resp.Rounds)
		}
		logging.Warnf("[%s] шаг %s: цикл исчерпал лимит раундов (%d), но чекпоинт отключён, продолжаю с частичным результатом", agentLabel(step.Agent, step.Role), step.ID, resp.Rounds)
	}

	if resp != nil {
		// Телеметрия: сколько раундов агентского цикла потратил шаг.
		e.trackStepMetrics(ctx, step.ID, map[string]any{"rounds": resp.Rounds})
	}

	return resp, nil
}

// runLeadStep выполняет шаг-декомпозицию лида направления и сразу реализует
// её: лид возвращает JSON-декомпозицию эпика на задачи (в plan-режиме доска
// не подключена, поэтому публикация необязательна), исполнитель печатает в
// консоль «Список задач лида» и выполняет каждую задачу агентом-специалистом
// (QA/DevOps или разработчик backend/frontend) строго в рамках scope шага.
// Так план, делегирующий разработку лидам, снова реально создаёт файлы.
func (e *Executor) runLeadStep(ctx context.Context, step *Step, projectName string) error {
	lead := newStepAgent(step, projectName)
	if lead == nil {
		return fmt.Errorf("неизвестный тип агента-лида: %s", step.Agent)
	}

	// Защитный откат на случай падения шага-декомпозиции (см. rollbackStep):
	// снимок покрывает и декомпозицию, и реализацию всех задач специалистами.
	// Снимок снят по области шага лида (leadStepScope): параллельные
	// шаги волны не пересекаются по файлам, поэтому откат одного из них не
	// станет откатывать чужую работу. PLAN.md исключается — им управляет
	// исполнитель.
	var snap *tools.Snap
	if rollbackEnabled() {
		var s *tools.Snap
		var serr error
		if scope := leadStepScope(projects.ProjectDir(projectName), step); len(scope) > 0 {
			s, serr = tools.NewSnapScoped(projects.ProjectDir(projectName), scope, planDocName)
		} else {
			s, serr = tools.NewSnap(projects.ProjectDir(projectName))
		}
		if serr == nil {
			snap = s
			logging.Detailf("[%s] шаг %s: защитный откат активен (снимок: %d файлов)", agentLabel(step.Agent, step.Role), step.ID, s.FileCount())
		} else {
			logging.Warnf("[%s] шаг %s: не удалось снять снимок для отката: %v", agentLabel(step.Agent, step.Role), step.ID, serr)
		}
	}

	// Лид scope НЕ ограничиваем: он только читает проект (List/ReadFiles) и
	// ведёт декомпозицию, пишущих инструментов у него нет. Сужать чтение до
	// step.Scope вредно — лиду нужно видеть проект целиком (в т.ч. корневой
	// README, соседние подпроекты монорепозитория), а «файл вне области
	// работы» на чтении лишь зацикливало шаг на ошибках. Специалистам, которые
	// выполняют задачи декомпозиции, scope ниже всё равно устанавливается.
	if rr, ok := lead.(interface{ SetRole(string) }); ok && step.Role != "" {
		rr.SetRole(string(step.Role))
	}

	// Шаг лида атомарен (декомпозиция + реализация): сохранённую историю
	// прошлого цикла отбрасываем, чтобы при resume шаг выполнился целиком,
	// а история лида не подменилась историей специалиста.
	if _, ok := e.loadResumeState(ctx, step.ID); ok {
		logging.Infof("[%s] шаг %s: сбрасываю устаревшую историю resume, шаг выполнится заново", agentLabel(step.Agent, step.Role), step.ID)
	}
	if e.store != nil {
		e.mu.Lock()
		if snap, err := e.store.Load(ctx); err == nil {
			if _, ok := snap.Conversations[step.ID]; ok {
				_ = e.store.ClearRoundState(ctx, snap, step.ID)
			}
		}
		e.mu.Unlock()
	}

	resp, err := e.provider.Generate(ctx, lead)
	if err != nil {
		e.rollbackStep(projectName, step.ID, snap)
		return fmt.Errorf("декомпозиция эпика %q: %w", step.ID, err)
	}
	if resp != nil && resp.Truncated {
		if err := e.truncatedStepError(ctx, step, resp); err != nil {
			return err
		}
	}

	tasks, err := board.UnmarshalTasks(resp.Content)
	if (err != nil || len(tasks) == 0) && !resp.Truncated {
		// Модель вместо JSON-декомпозиции вернула текст (пути, рассуждения):
		// не фаталим сразу — один раз спрашиваем ещё раз с чёткой инструкцией.
		// Повторный ответ обрабатываем как основной (петля «переспросить»
		// ограничена ровно одной попыткой, чтобы не растить токен-бюджет).
		logging.Warnf("[%s] шаг %s: лид не вернул JSON-декомпозицию (%v), переспрашиваю один раз",
			agentLabel(step.Agent, step.Role), step.ID, err)
		if retryResp, rerr := e.provider.Generate(ctx, leadWithHint(lead, resp.Content)); rerr == nil {
			resp = retryResp
			tasks, err = board.UnmarshalTasks(resp.Content)
		}
	}
	if err != nil || len(tasks) == 0 {
		e.rollbackStep(projectName, step.ID, snap)
		return fmt.Errorf("лид не вернул JSON-декомпозицию задач (задач: %d, ошибка: %v)", len(tasks), err)
	}
	printDecomposedTasks(tasks)
	sortTaskSpecs(tasks)
	step.Tasks = tasks
	e.persistPlan(ctx)

	// PLAN.md: детерминированно записываем полный план работ с декомпозицией
	// лидов — следующий разработчик видит все задачи и их контракты.
	if err := e.writePlan(projectName); err != nil {
		logging.Warnf("[%s] шаг %s: не удалось записать PLAN.md: %v", agentLabel(step.Agent, step.Role), step.ID, err)
	}

	for _, ts := range tasks {
		spec := specialistForRole(projectName, ts.AssignedRole, leadTaskPrompt(projectName, ts))
		// Специалисты, в отличие от лида, пишут код: ограничиваем их корневой
		// директорией шага (leadStepScope), а не точечным scope из плана —
		// планировщик указывает конкретные новые файлы (например,
		// frontend/package.json), а лид декомпозирует шаг на задачи, которые
		// трогают произвольные файлы всей области (компоненты, стили, сервисы).
		if scoper, ok := spec.(interface{ SetScope([]string) }); ok {
			scoper.SetScope(leadStepScope(projects.ProjectDir(projectName), step))
		}
		logging.Infof("[задача %s] специалист %s выполняет: %s", ts.TaskID, truncateText(ts.AssignedRole, 30), truncateText(ts.Title, 60))
		resp, err := e.provider.Generate(ctx, spec)
		if err != nil {
			e.rollbackStep(projectName, step.ID, snap)
			return fmt.Errorf("шаг %q, задача %s: %w", step.ID, ts.TaskID, err)
		}
		if resp != nil && resp.Truncated {
			if err := e.truncatedStepError(ctx, step, resp); err != nil {
				return err
			}
		}
		logging.Infof("[задача %s] выполнена специалистом %s", ts.TaskID, truncateText(ts.AssignedRole, 30))
	}
	// Финальная запись PLAN.md после выполнения всех задач шага.
	if err := e.writePlan(projectName); err != nil {
		logging.Warnf("[%s] шаг %s: не удалось записать PLAN.md (финальная): %v", agentLabel(step.Agent, step.Role), step.ID, err)
	}
	return nil
}

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
		logging.Warnf("[Checkpoint] не удалось сериализовать план с декомпозицией: %v", err)
		return
	}
	snap, err := e.store.Load(ctx)
	if err != nil {
		logging.Warnf("[Checkpoint] не удалось обновить PLAN.json (Load: %v)", err)
		return
	}
	snap.PlanJSON = planJSON
	if err := e.store.Save(ctx, snap); err != nil {
		logging.Warnf("[Checkpoint] не удалось обновить PLAN.json (Save: %v)", err)
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
func (e *Executor) truncatedStepError(ctx context.Context, step *Step, resp *runner.AgentResponse) error {
	if e.store != nil {
		if snap, err := e.store.Load(ctx); err == nil {
			_ = e.store.ClearRoundState(ctx, snap, step.ID)
		}
		return fmt.Errorf("шаг %q: исчерпан лимит раундов (%d) агентского цикла — запустите с --resume, шаг выполнится заново", step.ID, resp.Rounds)
	}
	if strings.TrimSpace(resp.Content) == "" {
		return fmt.Errorf("шаг %q: модель вернула пустой ответ (исчерпан лимит раундов: %d)", step.ID, resp.Rounds)
	}
	logging.Warnf("[%s] шаг %s: цикл исчерпал лимит раундов (%d), но чекпоинт отключён, продолжаю с частичным результатом", agentLabel(step.Agent, step.Role), step.ID, resp.Rounds)
	return nil
}

// leadTaskPrompt формирует задание специалисту по задаче из JSON-декомпозиции
// лида (plan-режим). В отличие от Kanban-задач, в plan-режиме доска не
// подключена, поэтому подсказки про статусы доски отсутствуют.
func leadTaskPrompt(project string, t board.TaskSpec) string {
	return fmt.Sprintf("Ты — специалист (%s), выполняешь задачу из декомпозиции лида проекта %q.\n\nЗадача: %s — %s\n\nПостановка задачи (полный контракт):\n%s\n\nПравила:\n- Работай в своей выходной директории (OutputDir): учи структуру через List, читай контракты через ReadFiles (в т.ч. PLAN.md в корне проекта — там полный план работ с контрактами всех задач лида).\n- Выполни задачу строго по описанному контракту: не меняй публичные сигнатуры, типы, интерфейсы и API-схемы из постановки.\n- Выполни задачу, прогони сборку и проверки через Run, доведи до зелёного состояния.\n- Не выходи за пределы своей части монорепозитория (роль задана промптом).",
		t.AssignedRole, project, t.TaskID, t.Title, t.Description)
}

// printDecomposedTasks выводит в консоль список задач из JSON-декомпозиции
// лида (plan-режим), чтобы человек видел итог декомпозиции до реализации.
func printDecomposedTasks(tasks []board.TaskSpec) {
	logging.Infof("[Список задач лида] %d задач:", len(tasks))
	for _, t := range tasks {
		logging.Infof("  - %s [%s] -> %s (порядок %d, параллельно: %v)",
			t.TaskID, truncateText(t.Title, 60), truncateText(t.AssignedRole, 30),
			t.SequenceOrder.Int(), t.CanRunParallel.Bool())
	}
}

// sortTaskSpecs сортирует задачи декомпозиции по sequence_order, затем по
// TaskID (детерминизм). Порядок последовательный: сначала выполняются
// инфраструктурные и базовые задачи, затем зависимые.
func sortTaskSpecs(tasks []board.TaskSpec) {
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i].SequenceOrder.Int(), tasks[j].SequenceOrder.Int()
		if a != b {
			return a < b
		}
		return tasks[i].TaskID < tasks[j].TaskID
	})
}

// loadResumeState читает сохранённую историю агентского цикла шага из
// чекпоинта. Возвращает состояние для продолжения цикла и true, если
// история есть. Возвращает false при resume=false, без чекпоинта или если
// истории для шага не сохранялось.
func (e *Executor) loadResumeState(ctx context.Context, stepID string) (*runner.ResumeState, bool) {
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
	var msgs []runner.Message
	if uerr := json.Unmarshal(conv, &msgs); uerr != nil {
		logging.Warnf("[Checkpoint] повреждена сохранённая история шага %s (%v), начинаю шаг заново", stepID, uerr)
		return nil, false
	}
	return &runner.ResumeState{Messages: msgs, Rounds: snap.Rounds[stepID]}, true
}

// persistRoundState сохраняет историю диалога и потраченные раунды шага в
// чекпоинт, чтобы при resume продолжить агентский цикл. Возвращает true,
// если состояние сохранено. Сериализуется e.mu: при параллельном выполнении
// волны истории двух шагов не должны терять обновления снимка.
func (e *Executor) persistRoundState(ctx context.Context, step *Step, resp *runner.AgentResponse) bool {
	if e.store == nil || len(resp.Messages) == 0 {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	conv, err := json.Marshal(resp.Messages)
	if err != nil {
		logging.Warnf("[Checkpoint] ошибка сериализации истории шага %s: %v", step.ID, err)
		return false
	}
	snap, err := e.store.Load(ctx)
	if err != nil {
		logging.Warnf("[Checkpoint] ошибка чтения чекпоинта для шага %s: %v", step.ID, err)
		return false
	}
	if err := e.store.SaveRoundState(ctx, snap, step.ID, resp.Rounds, conv); err != nil {
		logging.Warnf("[Checkpoint] ошибка сохранения истории шага %s: %v", step.ID, err)
		return false
	}
	return true
}

// runAcceptorAgent выполняет детерминированную приёмку собранного приложения:
// определяет тип проекта, собирает его, запускает на короткое время и
// анализирует логи. Сам шаг никогда не «падает» — отрицательный вердикт
// сохраняется в отчёт и обрабатывается циклом runAcceptanceLoop.
//
// Если scope шага указывает на один подкаталог (например frontend/ или
// server/) — приёмка выполняется в нём отдельно: у фронтенда и бэкенда
// собственная сборка и проверки. Пустой scope — приёмка всего корня
// (acceptor сам найдёт подпроекты и примет каждый по отдельности).
func (e *Executor) runAcceptorAgent(ctx context.Context, step *Step, projectName string) error {
	root := projects.ProjectDir(projectName)
	dir := acceptanceDir(root, step.Scope)
	rep := acceptor.Accept(dir, acceptor.LoadConfig())
	e.mu.Lock()
	e.acceptReports[step.ID] = rep
	e.mu.Unlock()

	// Телеметрия стадий приёмки в чекпоинт (verdict, статусы сборки/запуска/
	// форматирования/анализа). Чистая диагностика, на вердикт не влияет.
	m := map[string]any{
		"verdict": string(rep.Verdict),
		"dir":     dir,
	}
	if rep.Build.OK {
		m["build"] = "ok"
	} else {
		m["build"] = "fail"
	}
	if rep.Build.Skipped {
		m["build"] = "skipped"
	}
	if rep.Run != nil {
		m["run"] = "fail"
		if rep.Run.OK {
			m["run"] = "ok"
		}
		if rep.Run.Skipped {
			m["run"] = "skipped"
		}
	}
	if rep.Format != nil {
		m["format"] = "fail"
		if rep.Format.OK {
			m["format"] = "ok"
		}
	}
	if rep.Analyze != nil {
		m["analyze"] = "fail"
		if rep.Analyze.OK {
			m["analyze"] = "ok"
		}
	}
	e.trackStepMetrics(ctx, step.ID, m)

	if rep.Verdict == acceptor.VerdictApprove {
		logging.Infof("[приёмка] шаг %s: приёмка %q пройдена (%s)", step.ID, dir, rep.Summary)
	} else {
		logging.Infof("[приёмка] шаг %s: приёмка %q НЕ пройдена (%s)", step.ID, dir, rep.Summary)
		for _, iss := range rep.Issues {
			loc := iss.File
			if iss.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, iss.Line)
			}
			if loc != "" {
				logging.Detailf("[приёмка]   - [%s] %s %s", iss.Severity, loc, iss.Text)
			} else {
				logging.Detailf("[приёмка]   - [%s] %s", iss.Severity, iss.Text)
			}
		}
	}
	return nil
}

// acceptanceDir выбирает директорию приёмки для шага. Если scope шага сужается
// до одного существующего подкаталога (например ["frontend/"]) — приёмка идёт
// в нём; иначе — в корне проекта.
func acceptanceDir(root string, scope []string) string {
	top := topLevelScopeDir(scope)
	if top == "" {
		return root
	}
	candidate := filepath.Join(root, top)
	if st, err := os.Stat(candidate); err == nil && st.IsDir() {
		return candidate
	}
	return root
}

// topLevelScopeDir возвращает общую старшую директорию всех записей scope,
// если они все лежат в одном подкаталоге проекта ("frontend" для
// ["frontend/src/App.tsx", "frontend/package.json"]). Возвращает "" при разных
// подкаталогах, пустом scope или файле в корне.
func topLevelScopeDir(scope []string) string {
	seen := map[string]bool{}
	var dirs []string
	for _, s := range scope {
		s = strings.TrimPrefix(strings.TrimSpace(strings.ReplaceAll(s, "\\", "/")), "./")
		s = strings.Trim(strings.TrimSpace(s), "/")
		if s == "" {
			continue
		}
		first := s
		if i := strings.IndexByte(s, '/'); i >= 0 {
			first = s[:i]
		}
		if first == "." || first == ".." {
			continue
		}
		if !seen[first] {
			seen[first] = true
			dirs = append(dirs, first)
		}
	}
	if len(dirs) == 1 {
		return dirs[0]
	}
	return ""
}

// leadStepScope возвращает scope специалистов шага-декомпозиции лида.
// Планировщик даёт шагу точечный scope (конкретные новые файлы, например
// frontend/package.json), но лид декомпозирует шаг на задачи, затрагивающие
// произвольные файлы всей области (компоненты, стили, сервисы). Сужать
// специалистов до одного файла из плана нельзя — они не смогут писать свои
// файлы («вне области работы»). Поэтому специалистам выставляется корневая
// директория шага (первый сегмент: frontend/, server/), а не перечень файлов.
func leadStepScope(projectDir string, step *Step) []string {
	var scope []string
	if len(step.Scope) > 0 {
		if top := topLevelScopeDir(step.Scope); top != "" {
			scope = []string{top + "/"}
		} else {
			scope = effectiveCreationScope(projectDir, step.Scope)
		}
	}
	// PLAN.md всегда доступен специалистам: в нём полный план работ с
	// контрактами задач лидов, и каждый следующий разработчик должен его
	// прочитать, чтобы понять план и объём уже реализованного.
	scope = append(scope, planDocName)
	return scope
}

// effectiveCreationScope расширяет scope шага для СОЗДАНИЯ файлов. Если запись
// scope указывает на конкретный файл, которого ещё нет на диске, шаг создаёт
// его с нуля, и модель может выбрать фактическое имя (например, .js вместо
// .tsx из плана). Точное имя из плана в этом случае лишь зацикливает шаг на
// ошибках «вне области работы», и файлы не появляются. Поэтому для новых
// файлов scope расширяется до родительской директории; существующие файлы
// (шаги правки) остаются точечными, как задал планировщик.
func effectiveCreationScope(projectDir string, scope []string) []string {
	if len(scope) == 0 {
		return scope
	}
	out := make([]string, 0, len(scope))
	changed := false
	for _, s := range scope {
		normalized := strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
		trimmed := strings.TrimSuffix(normalized, "/")
		// Директории (со слэшем на конце) не трогаем.
		if trimmed != normalized || trimmed == "" {
			out = append(out, s)
			continue
		}
		full := filepath.Join(projectDir, filepath.FromSlash(trimmed))
		if _, err := os.Stat(full); os.IsNotExist(err) {
			if dir := filepath.ToSlash(filepath.Dir(trimmed)); dir != "." && dir != "" {
				out = append(out, dir+"/")
				changed = true
				continue
			}
		}
		out = append(out, s)
	}
	if !changed {
		return scope
	}
	return out
}

// runAcceptanceLoop — цикл «приёмка → планировщик исправлений → приёмка».
// Пока хотя бы один шаг acceptor плана не прошёл приёмку (и не исчерпан
// бюджет раундов ACCEPT_MAX_ROUNDS): отчёт приёмки передаётся планировщику,
// тот составляет шаги исправления разработчиками backend/frontend,
// исполнитель их прогоняет и повторяет приёмку.
func (e *Executor) runAcceptanceLoop(ctx context.Context) error {
	cfg := acceptor.LoadConfig()
	if cfg.MaxRounds <= 0 {
		return nil
	}

	// Шаги приёмки плана. Исправления выполняются сразу, без добавления
	// в волны: план остаётся неизменным.
	var acceptSteps []*Step
	for i := range e.plan.Steps {
		if e.plan.Steps[i].Agent == AgentQAEngineer {
			acceptSteps = append(acceptSteps, &e.plan.Steps[i])
		}
	}
	if len(acceptSteps) == 0 {
		return nil
	}

	for round := 1; round <= cfg.MaxRounds; round++ {
		var failing []*Step
		for _, s := range acceptSteps {
			switch rep := e.acceptReports[s.ID]; {
			case rep == nil:
				failing = append(failing, s)
			case rep.Verdict != acceptor.VerdictApprove:
				failing = append(failing, s)
			}
		}
		if len(failing) == 0 {
			logging.Infof("[приёмка] приёмка пройдена: все проекты соответствуют требованиям")
			return nil
		}

		for _, s := range failing {
			rep := e.acceptReports[s.ID]
			// Check if report is nil before proceeding with fixes
			if rep == nil {
				logging.Warnf("[приёмка] раунд %d/%d: отчёт приёмки отсутствует для шага %q", round, cfg.MaxRounds, s.Description)
				// Try to re-run acceptor for this step
				logging.Infof("[приёмка] раунд %d/%d: повторный запуск приёмки для шага %q", round, cfg.MaxRounds, s.Description)
				if err := e.runAcceptorAgent(ctx, s, e.plan.ProjectName); err != nil {
					return fmt.Errorf("раунд приёмки %d: повторный запуск приёмки для шага %q: %w", round, s.Description, err)
				}
				// Check again if the report is now available
				rep = e.acceptReports[s.ID]
				if rep == nil {
					return fmt.Errorf("раунд приёмки %d: отчёт приёмки всё ещё отсутствует для шага %q", round, s.Description)
				}
			}

			logging.Infof("[приёмка] раунд %d/%d: приёмка %q не пройдена — вызываю планировщик исправлений", round, cfg.MaxRounds, s.Description)

			fixes, err := e.planFixSteps(ctx, rep)
			if err != nil {
				return fmt.Errorf("раунд приёмки %d: планирование исправлений: %w", round, err)
			}
			if len(fixes) == 0 {
				logging.Warnf("[приёмка] раунд %d: планировщик не вернул шагов исправлений", round)
				return fmt.Errorf("раунд приёмки %d: планировщик не составил план исправлений для %q", round, s.Description)
			}

			// Выполняем шаги исправления (planning-результат), пропуская
			// неприменимые/лишние типы агентов.
			executed := 0
			for _, fs := range fixes {
				if fs.Agent == AgentQAEngineer {
					// Модель проигнорировала запрет: приёмку запускает цикл ниже.
					logging.Detailf("[приёмка] раунд %d: шаг qa в плане исправлений пропущен", round)
					continue
				}
				// Фикс-шаг не должен быть лид-типа: лид декомпозирует задачи, а в
				// цикле исправлений нужен один точечный правщик кода. Модель часто
				// возвращает backendlead/frontendlead — нормализуем в developer
				// соответствующей специализации по роли/подпроекту.
				switch fs.Agent {
				case AgentFrontendLead:
					logging.Detailf("[приёмка] раунд %d: лид-шаг %s приведён к frontend-developer", round, fs.Agent)
					fs.Agent = AgentFrontendDev
				case AgentBackendLead:
					logging.Detailf("[приёмка] раунд %d: лид-шаг %s приведён к backend-developer", round, fs.Agent)
					fs.Agent = AgentBackendDev
				}
				step := fs
				step.ID = fixStepID(e, round, executed)
				// Обновлённая область видимости: используем файлы из отчёта
				// приёмки, чтобы фикс-агента не заблокировала запись нужных
				// исходников. Если планировщик не указал scope — подставляем
				// файлы из отчёта.
				// Если приёмка вовсе не смогла указать конкретные файлы
				// (например, тип проекта ещё не определён — нет go.mod или
				// package.json, замечания без файлов) — scope принудительно
				// обнуляем: сужать область догадками планировщика нельзя,
				// иначе исправление упрётся в «вне области работы» и не
				// сможет записать нужные файлы.
				if files := rep.IssueFiles(); len(files) == 0 {
					step.Scope = nil
				} else if len(step.Scope) == 0 {
					step.Scope = files
				}
				// Переланировка добавляет задачу исправления в план: она
				// становится частью плана (findStep/чекпоинт/resume), а её
				// scope — обновлённый (файлы из отчёта приёмки).
				e.plan.Steps = append(e.plan.Steps, step)
				logging.Infof("[%s] раунд %d: шаг исправления %s: %s", agentLabel(step.Agent, step.Role), round, step.ID, step.Description)
				e.markRunning(ctx, step.ID)
				if err := e.executeStep(ctx, &step); err != nil {
					e.markFailed(ctx, step.ID)
					return fmt.Errorf("раунд приёмки %d, шаг исправления %q: %w", round, step.ID, err)
				}
				e.markDone(ctx, step.ID)
				executed++
			}
			if executed == 0 {
				return fmt.Errorf("раунд приёмки %d: планировщик не дал применимых шагов исправлений", round)
			}

			// Повторная приёмка после исправлений.
			logging.Infof("[приёмка] раунд %d: повторная приёмка после исправлений", round)
			if err := e.runAcceptorAgent(ctx, s, e.plan.ProjectName); err != nil {
				return err
			}
		}
	}

	logging.Warnf("[приёмка] исчерпан бюджет раундов приёмки (%d) — остались неисправленные замечания", cfg.MaxRounds)
	return fmt.Errorf("приёмка не пройдена после %d раундов исправлений, см. лог приёмки", cfg.MaxRounds)
}

// planFixSteps отдаёт планировщику отчёт приёмки и получает шаги исправления.
// Формат — тот же JSON-план, но планировщику запрещено добавлять acceptor:
// повторную приёмку запускает цикл приёмки исполнителя.
func (e *Executor) planFixSteps(ctx context.Context, rep *acceptor.Report) ([]Step, error) {
	// Обновлённая область видимости для задач исправления: исходит из тех
	// файлов, на которые реально указывает приёмка (acceptor не ограничен
	// областью видимости и видит весь модуль). Эти локации передаём
	// планировщику, чтобы scope шага покрывал файлы, которые придётся менять,
	// а не сузился до произвольного (например, [go.mod]) и не заблокировал
	// фикс-агента на записи нужных файлов.
	issueFiles := []string{}
	if rep != nil {
		issueFiles = uniqueSlash(rep.IssueFiles())
	}
	prompt := fmt.Sprintf(`Приёмка собранного приложения не пройдена.

%s

Карта уже реализованного кода проекта (существующие пакеты, типы и сигнатуры — НЕ дублируй и НЕ переписывай их, исправляй точечно):

%s

Составь план исправлений этих ошибок.
Требования:
- Все шаги — только агенты-разработчики backend или frontend, выбранные по принадлежности файлов к подпроекту (server/→backend, frontend/→frontend).
- НЕ добавляй шаг qa — повторную сборку, тестирование и приёмку запустит исполнитель.
- scope каждого шага — ТОЛЬКО файлы, реально требующие правки, обязательно включая:
%s
  (а не весь проект и не узкую догадку вроде только [go.mod], если правка нужна в исходниках).
- Каждый шаг — одно конкретное исправление.`, rep.FixPrompt(), BuildProjectMap(rep.Project), bullet(issueFiles))

	pa := NewPlanner(rep.Project, prompt)
	resp, err := e.provider.Generate(ctx, pa)
	if err != nil {
		return nil, err
	}
	fixPlan, err := ParsePlan(resp.Content)
	if err != nil {
		logging.Warnf("[планировщик] не удалось разобрать план исправлений: %v", err)
		logging.Detailf("[планировщик] Ответ планировщика:\n%s", resp.Content)
		return nil, err
	}
	return fixPlan.Steps, nil
}

// leadWithHint оборачивает агента-лида для повторного запроса декомпозиции:
// одна попытка на тот же промпт, но с подсказкой, что предыдущий ответ не был
// JSON. Новый контекст (чистый), поэтому подсказка кладётся в системные
// сообщения, чтобы модель сразу её видела.
func leadWithHint(lead agents.Agent, prevContent string) agents.Agent {
	hint := "Ваш предыдущий ответ не был распознан как JSON-декомпозиция задач.\n" +
		"Верните ТОЛЬКО допустимый JSON — массив объектов вида:\n" +
		`[{"title":"...","description":"...","role":"backend|frontend","files":["..."]}]` + "\n" +
		"без пояснений, markdown-обёрток и путей. Роль — одна из: backend, frontend, devops." +
		"\n\nВаш предыдущий ответ, чтобы не повторять его ошибки:\n" + prevContent
	return &leadHintAgent{inner: lead, hint: hint}
}

// leadHintAgent — декоратор agents.Agent, добавляющий подсказку к системным
// сообщениям лида (остальное делегирует внутреннему агенту).
type leadHintAgent struct {
	inner agents.Agent
	hint  string
}

func (h *leadHintAgent) GetSystemMessages(text []agents.Message) []agents.Message {
	base := h.inner.GetSystemMessages(text)
	return append(base, agents.Message{Type: agents.MessageTypeSystem, Message: h.hint})
}

func (h *leadHintAgent) GetUserMessages() []agents.Message { return h.inner.GetUserMessages() }
func (h *leadHintAgent) GetTools() []tools.ToolDefinition  { return h.inner.GetTools() }
func (h *leadHintAgent) GetToolsForOllama() []api.Tool     { return h.inner.GetToolsForOllama() }
func (h *leadHintAgent) CallFunction(name string, args map[string]any) ([]byte, error) {
	return h.inner.CallFunction(name, args)
}

// fixStepID генерирует уникальный ID шага исправления приёмки, чтобы не
// пересекаться с ID шагов основного плана и других раундов.
func fixStepID(e *Executor, round, idx int) string {
	base := fmt.Sprintf("accept-r%d-%d", round, idx)
	if e.findStep(base) == nil {
		return base
	}
	for n := 1; ; n++ {
		id := fmt.Sprintf("accept-r%d-%d-%d", round, idx, n)
		if e.findStep(id) == nil {
			return id
		}
	}
}

// uniqueSlash возвращает уникальные элементы списка, нормализуя пути к
// slash-формату.
func uniqueSlash(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		it = strings.TrimSpace(strings.ReplaceAll(it, "\\", "/"))
		if it == "" || seen[it] {
			continue
		}
		seen[it] = true
		out = append(out, it)
	}
	return out
}

// bullet форматирует список как маркированный отступ (пустой список → «—»).
func bullet(items []string) string {
	if len(items) == 0 {
		return "  - (файлы из отчёта приёмки)"
	}
	var b strings.Builder
	for _, it := range items {
		b.WriteString("  - ")
		b.WriteString(it)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// findStep находит шаг по ID.
func (e *Executor) findStep(id string) *Step {
	for i := range e.plan.Steps {
		if e.plan.Steps[i].ID == id {
			return &e.plan.Steps[i]
		}
	}
	return nil
}
