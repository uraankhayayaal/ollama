package planner

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ai/application/usecases"
	"ai/domain"
	"ai/domain/services"
	"ai/shared/logging"
	"ai/shared/projects"
)

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

	case AgentDevopsLead, AgentFrontendLead, AgentBackendLead:
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
func newStepAgent(step *Step, projectName string) services.Agent {
	return agentFactory().Step(step.Agent, projectName, step.Prompt)
}

// runCodingAgent запускает агента-разработчика (backend/frontend) или другого
// специалиста (devops, qa, лиды). Все агенты ограничены областью работы scope:
// пишут/читают только указанные файлы. Возвращает ответ модели (нужен циклу
// QA-багрепортов: observable из него парсятся репорты дефектов).
func (e *Executor) runCodingAgent(ctx context.Context, step *Step, projectName string) (*domain.AgentResponse, error) {
	// Подмешивание контекста RAG по шагу (Ф-4): семантическая выборка
	// релевантного кода по тексту шага с фильтром его scope. Клонируем шаг,
	// чтобы чекпоинт/PLAN.md хранили исходный промпт (без служебного блока);
	// RAG недоступен/выключен — блок пуст, шаг исполняется как раньше.
	if e.rag != nil {
		if blk := stepRAGContext(ctx, projectName, step, e.rag); blk != "" {
			cp := *step
			cp.Prompt = blk + "\n\n" + step.Prompt
			step = &cp
			e.log.Detailf("[%s] шаг %s: в промпт подмешан контекст RAG по scope", agentLabel(step.Agent, step.Role), step.ID)
		}
	}

	agent := newStepAgent(step, projectName)
	if agent == nil {
		return nil, fmt.Errorf("неизвестный тип агента: %s", step.Agent)
	}
	if rep := services.ReporterFromContext(ctx); rep != nil {
		ctx = services.WithReporter(ctx, rep.WithAgent(agentDisplayName(step.Agent)))
	}

	// Ограничиваем инструменты агента областью работы шага: вне scope он не
	// сможет ни читать, ни писать, ни удалять файлы. Для шагов создания scope
	// предварительно расширяется до директорий (см. effectiveCreationScope).
	scope := effectiveCreationScope(projects.ProjectDir(projectName), step.Scope)
	if len(scope) != len(step.Scope) {
		e.log.Detailf("[%s] шаг %s: scope шага расширен для создания: %v -> %v", agentLabel(step.Agent, step.Role), step.ID, step.Scope, scope)
	}

	// Снимок проекта перед выполнением шага: при реальной ошибке шага его
	// область откатывается к этому состоянию, чтобы сломанные правки упавшего
	// субагента (в т.ч. удалённый/переписанный им чужой функционал) не утекли
	// в общий репозиторий (см. rollbackEnabled/rollbackStep). Снимок СКОПИРОВАН
	// по области шага (NewSnapScoped): при параллельном выполнении волны откат
	// одного шага не должен трогать файлы соседних. PLAN.md исключается — им
	// управляет исполнитель, и параллельный шаг не удаляет его при откате.
	var snap services.Snapshot
	if rollbackEnabled() {
		var s services.Snapshot
		var serr error
		if e.snapshotFactory == nil {
			serr = fmt.Errorf("фабрика снимков не подключена")
		} else if len(scope) > 0 {
			s, serr = e.snapshotFactory.NewScoped(projects.ProjectDir(projectName), scope, planDocName)
		} else {
			s, serr = e.snapshotFactory.New(projects.ProjectDir(projectName))
		}
		if serr == nil {
			snap = s
			e.log.Detailf("[%s] шаг %s: защитный откат активен (снимок: %d файлов, scope %d записей)", agentLabel(step.Agent, step.Role), step.ID, s.FileCount(), len(scope))
		} else {
			e.log.Warnf("[%s] шаг %s: не удалось снять снимок для отката: %v", agentLabel(step.Agent, step.Role), step.ID, serr)
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
		e.log.Detailf("[%s] шаг %s: возобновляю агентский цикл с раунда %d (повторный запуск с --resume)", agentLabel(step.Agent, step.Role), step.ID, rs.Rounds+1)
		genCtx = usecases.WithResumeState(ctx, rs)
	}

	// Снимок публичной поверхности (нормализованные сигнатуры пакетов) до шага.
	// После выполнения diff покажет, как изменился контракт — добавление/
	// удаление/изменение публичных деклараций. Не ошибка плана, а сигнал для
	// человека (см. auditContractStep); при CODEGEN_CONTRACT_ENFORCE=1 уровень
	// лога поднимается до предупреждения.
	var apiBefore *services.PublicAPISnapshot
	if contractAuditEnabled() && e.apiAuditor != nil {
		if s, serr := e.apiAuditor.Snapshot(projects.ProjectDir(projectName)); serr == nil {
			apiBefore = s
		} else {
			e.log.Detailf("[%s] шаг %s: не удалось снять снимок API: %v", agentLabel(step.Agent, step.Role), step.ID, serr)
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

	// Scope-гейт ЛСП (Ф-5): нативные диагностики строго по изменённым шагом
	// файлам. Список берётся из diff снимка (added+modified); без снимка
	// (откат выключен) — фолбэк на развёрнутый scope. Изменений нет — гейт
	// молчит. Если субагент оставил код сломанным в своей области — шаг
	// считается упавшим (откат области). Сервер не установлен → гейт неактивен.
	var gateFiles []string
	if snap != nil {
		added, modified, _, derr := snap.Diff()
		if derr == nil {
			gateFiles = append(added, modified...)
		}
	} else if len(scope) > 0 {
		gateFiles = scopeSourceFiles(projects.ProjectDir(projectName), scope, maxScopeFiles)
	}
	if err := e.checkStepLSP(ctx, step, projects.ProjectDir(projectName), gateFiles); err != nil {
		e.rollbackStep(projectName, step.ID, snap)
		return nil, err
	}

	// Агент зациклился (разрыв петли не помог): историю в чекпоинт НЕ
	// сохраняем — resume продолжил бы тот же застрявший диалог и повторил
	// петлю с новым бюджетом раундов. Шаг падает явно, чтобы его взяли с другой
	// стороны (другая модель/новый запуск), а не «доработали» тем же способом.
	if err := resp.LoopError(fmt.Sprintf("шаг %q", step.ID)); err != nil {
		if e.store != nil {
			e.clearRoundState(ctx, step)
		}
		return nil, err
	}

	// Цикл упёрся в лимит раундов (или модель обрезалась по лимиту токенов):
	// сохраняем историю диалога в чекпоинт, чтобы следующий запуск с --resume
	// продолжил шаг с раунда resp.Rounds+1, и останавливаем выполнение плана.
	if resp != nil && resp.Truncated {
		if e.persistRoundState(ctx, step, resp) {
			e.log.Warnf("[%s] шаг %s: истощён лимит раундов (%d), история сохранена в чекпоинт", agentLabel(step.Agent, step.Role), step.ID, resp.Rounds)
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
		e.log.Warnf("[%s] шаг %s: цикл исчерпал лимит раундов (%d), но чекпоинт отключён, продолжаю с частичным результатом", agentLabel(step.Agent, step.Role), step.ID, resp.Rounds)
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
	var snap services.Snapshot
	if rollbackEnabled() {
		var s services.Snapshot
		var serr error
		scope := leadStepScope(projects.ProjectDir(projectName), step)
		if e.snapshotFactory == nil {
			serr = fmt.Errorf("фабрика снимков не подключена")
		} else if len(scope) > 0 {
			s, serr = e.snapshotFactory.NewScoped(projects.ProjectDir(projectName), scope, planDocName)
		} else {
			s, serr = e.snapshotFactory.New(projects.ProjectDir(projectName))
		}
		if serr == nil {
			snap = s
			e.log.Detailf("[%s] шаг %s: защитный откат активен (снимок: %d файлов)", agentLabel(step.Agent, step.Role), step.ID, s.FileCount())
		} else {
			e.log.Warnf("[%s] шаг %s: не удалось снять снимок для отката: %v", agentLabel(step.Agent, step.Role), step.ID, serr)
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
		e.log.Infof("[%s] шаг %s: сбрасываю устаревшую историю resume, шаг выполнится заново", agentLabel(step.Agent, step.Role), step.ID)
	}
	e.clearRoundState(ctx, step)

	leadCtx := ctx
	if rep := services.ReporterFromContext(leadCtx); rep != nil {
		leadCtx = services.WithReporter(leadCtx, rep.WithAgent(agentDisplayName(step.Agent)))
	}
	resp, err := e.provider.Generate(leadCtx, lead)
	if err != nil {
		e.rollbackStep(projectName, step.ID, snap)
		return fmt.Errorf("декомпозиция эпика %q: %w", step.ID, err)
	}
	if err := resp.LoopError(fmt.Sprintf("декомпозиция эпика %q", step.ID)); err != nil {
		e.clearRoundState(ctx, step)
		return err
	}
	if resp != nil && resp.Truncated {
		if err := e.truncatedStepError(ctx, step, resp); err != nil {
			return err
		}
	}

	tasks, err := domain.UnmarshalTasks(resp.Content)
	if (err != nil || len(tasks) == 0) && !resp.Truncated {
		// Модель вместо JSON-декомпозиции вернула текст (пути, рассуждения):
		// не фаталим сразу — один раз спрашиваем ещё раз с чёткой инструкцией.
		// Повторный ответ обрабатываем как основной (петля «переспросить»
		// ограничена ровно одной попыткой, чтобы не растить токен-бюджет).
		e.log.Warnf("[%s] шаг %s: лид не вернул JSON-декомпозицию (%v), переспрашиваю один раз",
			agentLabel(step.Agent, step.Role), step.ID, err)
		if retryResp, rerr := e.provider.Generate(ctx, leadWithHint(lead, resp.Content)); rerr == nil {
			resp = retryResp
			tasks, err = domain.UnmarshalTasks(resp.Content)
		}
	}
	if err != nil || len(tasks) == 0 {
		e.rollbackStep(projectName, step.ID, snap)
		return fmt.Errorf("лид не вернул JSON-декомпозицию задач (задач: %d, ошибка: %v)", len(tasks), err)
	}
	printDecomposedTasks(e.log, tasks)
	sortTaskSpecs(tasks)
	step.Tasks = toDomainTaskSpecs(tasks)
	e.persistPlan(ctx)

	// PLAN.md: детерминированно записываем полный план работ с декомпозицией
	// лидов — следующий разработчик видит все задачи и их контракты.
	if err := e.writePlan(projectName); err != nil {
		e.log.Warnf("[%s] шаг %s: не удалось записать PLAN.md: %v", agentLabel(step.Agent, step.Role), step.ID, err)
	}

	for _, ts := range tasks {
		spec := SpecialistForRole(projectName, ts.AssignedRole, leadTaskPrompt(projectName, ts))
		// Специалисты, в отличие от лида, пишут код: ограничиваем их корневой
		// директорией шага (leadStepScope), а не точечным scope из плана —
		// планировщик указывает конкретные новые файлы (например,
		// frontend/package.json), а лид декомпозирует шаг на задачи, которые
		// трогают произвольные файлы всей области (компоненты, стили, сервисы).
		if scoper, ok := spec.(interface{ SetScope([]string) }); ok {
			scoper.SetScope(leadStepScope(projects.ProjectDir(projectName), step))
		}
		e.log.Infof("[задача %s] специалист %s выполняет: %s", ts.TaskID, truncateText(ts.AssignedRole, 30), truncateText(ts.Title, 60))
		specCtx := ctx
		specName := chatNameForSpec(spec, ts.AssignedRole)
		if rep := services.ReporterFromContext(specCtx); rep != nil {
			specCtx = services.WithReporter(specCtx, rep.WithAgent(specName))
		}
		resp, err := e.provider.Generate(specCtx, spec)
		if err != nil {
			e.rollbackStep(projectName, step.ID, snap)
			return fmt.Errorf("шаг %q, задача %s: %w", step.ID, ts.TaskID, err)
		}
		if err := resp.LoopError(fmt.Sprintf("шаг %q, задача %s", step.ID, ts.TaskID)); err != nil {
			e.clearRoundState(ctx, step)
			return err
		}
		if resp != nil && resp.Truncated {
			if err := e.truncatedStepError(ctx, step, resp); err != nil {
				return err
			}
		}
		e.log.Infof("[задача %s] выполнена специалистом %s", ts.TaskID, truncateText(ts.AssignedRole, 30))
	}
	// Финальная запись PLAN.md после выполнения всех задач шага.
	if err := e.writePlan(projectName); err != nil {
		e.log.Warnf("[%s] шаг %s: не удалось записать PLAN.md (финальная): %v", agentLabel(step.Agent, step.Role), step.ID, err)
	}
	return nil
}

// leadTaskPrompt формирует задание специалисту по задаче из JSON-декомпозиции
// лида (plan-режим). В отличие от Kanban-задач, в plan-режиме доска не
// подключена, поэтому подсказки про статусы доски отсутствуют.
func leadTaskPrompt(project string, t domain.TaskSpec) string {
	return fmt.Sprintf("Ты — специалист (%s), выполняешь задачу из декомпозиции лида проекта %q.\n\nЗадача: %s — %s\n\nПостановка задачи (полный контракт):\n%s\n\nПравила:\n- Работай в своей выходной директории (OutputDir): учи структуру через List, читай контракты через ReadFiles (в т.ч. PLAN.md в корне проекта — там полный план работ с контрактами всех задач лида).\n- Выполни задачу строго по описанному контракту: не меняй публичные сигнатуры, типы, интерфейсы и API-схемы из постановки.\n- Выполни задачу, прогони сборку и проверки через Run, доведи до зелёного состояния.\n- Не выходи за пределы своей части монорепозитория (роль задана промптом).",
		t.AssignedRole, project, t.TaskID, t.Title, t.Description)
}

// printDecomposedTasks выводит в консоль список задач из JSON-декомпозиции
// лида (plan-режим), чтобы человек видел итог декомпозиции до реализации.
func printDecomposedTasks(log *logging.Logger, tasks []domain.TaskSpec) {
	log.Infof("[Список задач лида] %d задач:", len(tasks))
	for _, t := range tasks {
		log.Infof("  - %s [%s] -> %s (порядок %d, параллельно: %v)",
			t.TaskID, truncateText(t.Title, 60), truncateText(t.AssignedRole, 30),
			t.SequenceOrder.Int(), t.CanRunParallel.Bool())
	}
}

// sortTaskSpecs сортирует задачи декомпозиции по sequence_order, затем по
// TaskID (детерминизм). Порядок последовательный: сначала выполняются
// инфраструктурные и базовые задачи, затем зависимые.
func sortTaskSpecs(tasks []domain.TaskSpec) {
	sort.SliceStable(tasks, func(i, j int) bool {
		a, b := tasks[i].SequenceOrder.Int(), tasks[j].SequenceOrder.Int()
		if a != b {
			return a < b
		}
		return tasks[i].TaskID < tasks[j].TaskID
	})
}
