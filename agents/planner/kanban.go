package planner

import (
	"ai/agents"
	"ai/agents/architect"
	"ai/agents/backendlead"
	"ai/agents/codegenerator"
	"ai/agents/devops"
	"ai/agents/devopslead"
	"ai/agents/frontendlead"
	"ai/agents/qaengineer"
	"ai/agents/qalead"
	"ai/agents/refactor"
	"ai/board"
	"ai/logging"
	"ai/models"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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
// НЕ запускают консольные команды — они только генерируют JSON-декомпозицию
// эпика на задачи (read-only инструменты List/ReadFiles).
type KanbanRunner struct {
	provider models.LLMProvider
	store    *board.Store
}

// NewKanbanRunner создаёт Kanban-оркестратор поверх хранилища доски.
func NewKanbanRunner(provider models.LLMProvider, store *board.Store) *KanbanRunner {
	return &KanbanRunner{provider: provider, store: store}
}

// Run исполняет Kanban-оркестрацию до решения задачи пользователя.
func (k *KanbanRunner) Run(ctx context.Context, projectName, taskText string) error {
	if err := k.ensureMeta(ctx, projectName, taskText); err != nil {
		return err
	}

	for round := 1; round <= maxKanbanRounds; round++ {
		// Задача решена: все эпики и все задачи успешно выполнены.
		done, err := k.store.AllDone(ctx)
		if err != nil {
			return err
		}
		if done {
			if meta, gerr := k.store.GetMeta(ctx); gerr == nil {
				meta.Status = board.StatusDone
				_ = k.store.SaveMeta(ctx, meta)
			}
			logging.Infof("[Kanban] задача пользователя решена: все эпики и задачи выполнены")
			return nil
		}

		progress := false
		for _, phase := range []func(context.Context) (bool, error){
			k.phaseArchitect,
			k.phaseLeads,
			k.phaseReady,
			k.phaseExecute,
			k.phaseBugs,
			k.phaseComplete,
		} {
			p, err := phase(ctx)
			if err != nil {
				return err
			}
			progress = progress || p
		}

		// Ни один этап цикла не сделал работу: на доске остались только
		// отменённые записи или неразрешимые зависимости — дальше бессмысленно.
		if !progress {
			return fmt.Errorf("Kanban-цикл %d: нет прогресса (остались отменённые/заблокированные эпики и задачи), доска: %s",
				round, k.boardSummary(ctx))
		}
	}

	return fmt.Errorf("исчерпан бюджет Kanban-раундов (%d), задача не решена", maxKanbanRounds)
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
		logging.Infof("[Kanban] доска проекта %q создана: %s", projectName, truncateText(taskText, 80))
	}
	if meta.Status != board.StatusDone {
		meta.Status = board.StatusInProgress
		return k.store.SaveMeta(ctx, meta)
	}
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
	resp, err := k.provider.Generate(ctx, arch)
	if err != nil {
		return false, fmt.Errorf("фаза архитектора: %w", err)
	}
	if resp != nil && resp.Truncated {
		return false, fmt.Errorf("фаза архитектора: цикл остановлен по лимиту раундов")
	}

	epics, err = k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	if len(epics) == 0 {
		return false, fmt.Errorf("фаза архитектора: модель не опубликовала эпики (submit_architecture_backlog не вызван)")
	}
	logging.Infof("[Kanban] архитектор опубликовал %d эпиков: %s", len(epics), epicList(epics))
	return true, nil
}

// phaseLeads: декомпозиция и ревизия эпиков лидами направлений. За один цикл
// обрабатываем один эпик (дорогой вызов модели). Лид работает инструментами
// доски (BoardCreateTask и др., если они доступны); при недоступности доски —
// JSON-декомпозицией (fallback). Эпик переводится в «в анализе»; после ревизии
// LeadSyncedRev синхронизируется с ревизией эпика (пересмотр задач при
// изменении контрактов архитектором, Revision > LeadSyncedRev).
func (k *KanbanRunner) phaseLeads(ctx context.Context) (bool, error) {
	epics, err := k.store.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	for _, epic := range epics {
		// Терминальные эпики не трогаем.
		if epic.Status.Terminal() {
			continue
		}
		// Нужна первичная декомпозиция (задач нет) либо ревизия после изменения
		// эпика Системным архитектором (ревизия доски выросла).
		needDecompose := len(epic.Tasks) == 0
		needResync := !needDecompose && epic.Revision > epic.LeadSyncedRev
		if !needDecompose && !needResync {
			continue
		}

		lead, err := k.leadFor(epic)
		if err != nil {
			return false, fmt.Errorf("фаза лидов, эпик %s: %w", epic.TaskID, err)
		}
		if sb, ok := lead.(interface{ SetBoardStore(*board.Store) }); ok {
			sb.SetBoardStore(k.store)
		}

		// «В анализе»: для новой декомпозиции обязателен; при ревизии эпис может
		// находиться дальше по цепочке — некритично (переход в анализ не требуется).
		if err := k.store.SetEpicStatus(ctx, epic.TaskID, board.StatusAnalysis); err != nil {
			if _, ok := err.(*board.StatusError); !ok {
				return false, fmt.Errorf("эпик %s: перевод в «в анализе»: %w", epic.TaskID, err)
			}
		}
		logging.Infof("[Kanban] декомпозиция/ревизия эпика %s (%s) лидом %s (нужна ревизия: %v)",
			epic.TaskID, epic.Title, leadName(epic), needResync)

		resp, err := k.provider.Generate(ctx, lead)
		if err != nil {
			return false, fmt.Errorf("декомпозиция эпика %s: %w", epic.TaskID, err)
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("декомпозиция эпика %s: цикл остановлен по лимиту раундов", epic.TaskID)
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
					return false, fmt.Errorf("декомпозиция эпика %s: лид не создал задач ни доской, ни JSON: %w", epic.TaskID, err)
				}
				if len(dec) == 0 {
					return false, fmt.Errorf("декомпозиция эпика %s: лид вернул пустой список tasks", epic.TaskID)
				}
				for i := range dec {
					ts := dec[i]
					if err := k.store.CreateTask(ctx, &board.Task{TaskSpec: ts, EpicID: epic.TaskID, Assignee: assignee(ts)}); err != nil {
						return false, fmt.Errorf("эпик %s: создание задачи %s: %w", epic.TaskID, ts.TaskID, err)
					}
				}
				logging.Infof("[Kanban] эпик %s декомпозирован на %d задач (JSON-fallback)", epic.TaskID, len(dec))
			} else {
				logging.Infof("[Kanban] эпик %s декомпозирован на %d задач (инструменты доски)", epic.TaskID, len(tasks))
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
		logging.Infof("[Kanban] эпик %s готов (ревизия %d, синхронизировано лидом %d)", epic.TaskID, synced.Revision, synced.LeadSyncedRev)
		return true, nil
	}
	return false, nil
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
		ok, err := k.depsDone(ctx, t)
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
		ok, err := k.phasePrereqSatisfied(ctx, t)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusReady); err != nil {
			return false, fmt.Errorf("задача %s: в анализе -> готова к работе: %w", t.TaskID, err)
		}
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
			logging.Detailf("[Kanban] специалист %s уже занят в этом цикле — задача %s ждёт", t.Assignee, t.TaskID)
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

		logging.Infof("[Kanban] выполняю задачу %s (%s) специалистом %s",
			t.TaskID, truncateText(t.Title, 60), t.Assignee)
		resp, err := k.provider.Generate(ctx, specialist)
		if err != nil {
			return false, fmt.Errorf("задача %s: %w", t.TaskID, err)
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("задача %s: цикл остановлен по лимиту раундов", t.TaskID)
		}

		if err := k.store.SetTaskStatus(ctx, t.TaskID, board.StatusDone); err != nil {
			return false, fmt.Errorf("задача %s: в работе -> выполнена: %w", t.TaskID, err)
		}
		logging.Infof("[Kanban] задача %s выполнена", t.TaskID)
		progress = true
	}
	return progress, nil
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
		resp, err := k.provider.Generate(ctx, qa)
		if err != nil {
			return false, fmt.Errorf("фаза триажа багрепортов: %w", err)
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("фаза триажа багрепортов: цикл остановлен по лимиту раундов")
		}
		logging.Infof("[Kanban] триаж %d багрепортов QA Lead", len(newBugs))
		progress = true
	}

	// Экспертиза Системного архитектора: вердикт по подтверждённым багрепортам.
	if len(confirmedBugs) > 0 {
		arch := architect.NewArchitectWithStore(k.store.Project(),
			k.bugExpertPrompt(k.store.Project(), confirmedBugs), k.store).AsBugExpert()
		resp, err := k.provider.Generate(ctx, arch)
		if err != nil {
			return false, fmt.Errorf("фаза экспертизы багрепортов: %w", err)
		}
		if resp != nil && resp.Truncated {
			return false, fmt.Errorf("фаза экспертизы багрепортов: цикл остановлен по лимиту раундов")
		}
		logging.Infof("[Kanban] экспертиза %d багрепортов архитектором", len(confirmedBugs))
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
		if len(epic.Tasks) == 0 {
			continue
		}
		allDone := true
		for _, taskID := range epic.Tasks {
			t, err := k.store.GetTask(ctx, taskID)
			if err != nil {
				return false, fmt.Errorf("эпик %s: чтение задачи %s: %w", epic.TaskID, taskID, err)
			}
			if t.Status != board.StatusDone {
				allDone = false
				break
			}
		}
		if allDone {
			// «Доводим» эпик до «выполнена» из любого не-терминального статуса
			// по цепочке new -> analysis -> ready -> in_progress -> done. Новая
			// задача продвигает последующие; статусы менеджер переместил вручную
			// (resume) — по цепочке с текущего места.
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
				logging.Infof("[Kanban] эпик %s (%s) выполнен", epic.TaskID, epic.Title)
				progress = true
				// Багрепорты, направленные на эпик исправления, закрываются
				// (fix -> fixed): исправление поставлено и проверено.
				if n, err := k.store.MarkBugsFixedForEpic(ctx, epic.TaskID); err != nil {
					return false, fmt.Errorf("эпик %s: закрытие багрепортов: %w", epic.TaskID, err)
				} else if n > 0 {
					logging.Infof("[Kanban] эпик %s закрыл %d багрепортов", epic.TaskID, n)
					progress = true
				}
			}
		}
	}
	return progress, nil
}

// noteEpicProgress переводит эпик в «в работе», если он в статусе
// «готова к работе» (а его задачи начали выполняться).
func (k *KanbanRunner) noteEpicProgress(ctx context.Context, epicID string) {
	epic, err := k.store.GetEpic(ctx, epicID)
	if err != nil || epic.Status != board.StatusReady {
		return
	}
	if err := k.store.SetEpicStatus(ctx, epicID, board.StatusInProgress); err != nil {
		logging.Detailf("[Kanban] эпик %s: в работу: %v", epicID, err)
	}
}

// readyTasks возвращает задачи «готова к работе», отсортированные по порядку
// (sequence_order) и ID.
func (k *KanbanRunner) readyTasks(ctx context.Context) ([]*board.Task, error) {
	tasks, err := k.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var ready []*board.Task
	for _, t := range tasks {
		if t.Status == board.StatusReady {
			ready = append(ready, t)
		}
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

// boardSummary собирает краткую сводку доски для логов и ошибок.
func (k *KanbanRunner) boardSummary(ctx context.Context) string {
	ec, _ := k.store.EpicCounts(ctx)
	tc, _ := k.store.TaskCounts(ctx)
	return fmt.Sprintf("эпиков: %d (выполнено %d, отменено %d), задач: %d (выполнено %d, отменено %d)",
		ec.Total, ec.By[board.StatusDone], ec.By[board.StatusCancelled],
		tc.Total, tc.By[board.StatusDone], tc.By[board.StatusCancelled])
}

// leadFor создаёт агента-лида направления по assigned_role эпика и собирает
// промпт декомпозиции. Лиды не пишут код и не запускают команды.
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
func (k *KanbanRunner) leadPrompt(epic *board.Epic) string {
	var b strings.Builder
	b.WriteString("Декомпозируй эпик из бэклога Системного архитектора на задачи для рядовых специалистов.\n\n")
	fmt.Fprintf(&b, "Проект: %s\n", k.store.Project())
	fmt.Fprintf(&b, "Эпик: %s — %s\n", epic.TaskID, epic.Title)
	if epic.Summary != "" {
		fmt.Fprintf(&b, "Архитектурная сводка:\n%s\n\n", epic.Summary)
	}
	fmt.Fprintf(&b, "Описание эпика:\n%s\n\n", epic.Description)
	b.WriteString("Публикация задач:\n")
	b.WriteString("- Если у тебя есть инструменты доски (BoardCreateTask и др.) — создавай задачи ими (полный контракт в description, assigned_role, sequence_order, dependencies). Обновляй и удаляй задачи через BoardUpdateTask/BoardDeleteTask (кроме взятых в работу).\n")
	b.WriteString("- Если инструментов доски нет — верни строго JSON-декомпозицию (без markdown-обёрток) по схеме:\n")
	b.WriteString("{\n")
	b.WriteString(`  "lead_summary": "краткое техническое описание модуля",` + "\n")
	b.WriteString(`  "tasks": [` + "\n")
	b.WriteString("    {\n")
	b.WriteString(`      "task_id": "уникальный ID (например T-01)",` + "\n")
	b.WriteString(`      "title": "название задачи",` + "\n")
	b.WriteString(`      "description": "детальное техническое описание задачи с готовым контрактом взаимодействия, который ты спроектировал",` + "\n")
	b.WriteString(`      "assigned_role": "роль специалиста (например Senior Go Developer / React Developer / QA Engineer / DevOps Engineer)",` + "\n")
	b.WriteString("      \"sequence_order\": 1,\n")
	b.WriteString(`      "can_run_parallel": true,` + "\n")
	b.WriteString(`      "dependencies": [` + "\n")
	b.WriteString("      ]\n")
	b.WriteString("    }\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n\n")
	b.WriteString("Правила:\n")
	b.WriteString("- Ты НЕ пишешь код и НЕ запускаешь команды: только проектируешь контракты и раздаёшь задачи.\n")
	b.WriteString("- Порядок разработки: сначала инфраструктура, затем приложение, затем тестирование — проставляй sequence_order и зависимости так, чтобы этот порядок соблюдался (тестовые задачи зависят от прикладных, прикладные — от инфраструктурных).\n")
	b.WriteString("- Контракты взаимодействия дублируй в описание каждой связанной задачи (единый источник истины).\n")
	b.WriteString("- Чётко проставь sequence_order и dependencies: какие задачи параллельны (can_run_parallel: true), какие блокируют друг друга.\n")
	return b.String()
}

// specialistFor создаёт агента-специалиста для задачи: QA/DevOps или
// разработчик (backend/frontend). Для пустого проекта используется генератор
// кода, для существующего — рефактор с ролью.
func (k *KanbanRunner) specialistFor(t *board.Task) (agents.Agent, error) {
	project := k.store.Project()
	prompt := k.taskPrompt(t)

	switch {
	case isRole(t.AssignedRole, "qa", "тест", "testing"):
		return qaengineer.NewQAEngineer(project, prompt), nil
	case isRole(t.AssignedRole, "devops", "инфра", "infra", "sre", "docker", "k8s", "ci"):
		return devops.NewDevops(project, prompt), nil
	}

	// Разработка кода: пустой проект создаёт генератор, существующий
	// дорабатывает рефактор с ролью (frontend/backend), чтобы изолировать
	// монорепозиторий.
	if projectDirEmpty(project) {
		return codegenerator.NewCodegenerator(project, prompt), nil
	}
	ra, err := refactor.NewRefactorAgent(prompt, project)
	if err != nil {
		return nil, err
	}
	if isRole(t.AssignedRole, "front", "react", "ui", "client", "фронт") {
		ra.SetRole("frontend")
	} else {
		ra.SetRole("backend")
	}
	return ra, nil
}

// taskPrompt формирует задание специалисту по задаче с Kanban-доски.
func (k *KanbanRunner) taskPrompt(t *board.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ты — специалист (%s), выполняешь задачу с общей Kanban-доски проекта %q.\n\n", t.AssignedRole, k.store.Project())
	fmt.Fprintf(&b, "Задача на доске: %s — %s\n\n", t.TaskID, t.Title)
	b.WriteString("Постановка задачи:\n")
	b.WriteString(t.Description)
	b.WriteString("\n\n")
	b.WriteString("Правила:\n")
	b.WriteString("- Работай в своей выходной директории (OutputDir): учи структуру через List, читай контракты через ReadFiles.\n")
	b.WriteString("- Выполни задачу, прогони сборку и проверки через Run, доведи до зелёного состояния.\n")
	b.WriteString("- Не выходи за пределы своей части монорепозитория (роль задана промптом).\n")
	fmt.Fprintf(&b, "- Если у тебя есть инструменты доски: идентификатор задачи %s. Ты можешь читать её контракт (BoardGetTask) и обновлять статус (BoardSetTaskStatus); о завершении задачи оркестратор позаботится сам.\n", t.TaskID)
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

// projectDirEmpty сообщает, существует ли уже проект temp/<name> с файлами.
func projectDirEmpty(project string) bool {
	dir := codegenerator.ProjectDir(project)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return true
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
