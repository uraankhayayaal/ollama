package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/redis/go-redis/v9"
)

// CreateTask добавляет задачу на доску. Задача обязательно привязывается к
// существующему эпику (EpicID), ID (TaskID) должен быть уникален в пределах
// проекта. Зависимости обязаны ссылаться на уже существующие записи
// доски (задачи или эпики) — это гарантирует корректность связей.
func (s *Store) CreateTask(ctx context.Context, t *Task) error {
	if t.TaskID == "" {
		return fmt.Errorf("board: task_id обязателен")
	}
	if t.EpicID == "" {
		return fmt.Errorf("board: задача %q должна ссылаться на эпик (epic_id)", t.TaskID)
	}
	t.ProjectName = s.project
	s.applyRepositories(&t.Repositories)

	if _, err := s.GetEpic(ctx, t.EpicID); err != nil {
		return &ErrDependency{Ref: "epic:" + t.EpicID}
	}
	if ok, err := s.client.SIsMember(ctx, s.tasksID(), t.TaskID).Result(); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: задача %q", ErrExists, t.TaskID)
	}
	for _, dep := range t.Dependencies {
		if err := s.referenceExists(ctx, dep); err != nil {
			return &ErrDependency{Ref: dep}
		}
	}

	t.Status = FirstNonZeroStatus(t.Status, StatusInfra)
	t.CreatedAt = now()
	t.UpdatedAt = t.CreatedAt
	// Ф-5: прогноз расхода токенов по истории завершённых единиц (если задан).
	if t.TokenEstimate == 0 && s.TokenEstimate != nil {
		if v := s.TokenEstimate(ctx, nil, t); v > 0 {
			t.TokenEstimate = v
		}
	}

	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err := s.client.Set(ctx, s.taskKey(t.TaskID), data, s.ttl).Err(); err != nil {
		return err
	}
	if err := s.client.SAdd(ctx, s.tasksID(), t.TaskID).Err(); err != nil {
		return err
	}

	// Двусторонняя связь: задача ссылается на эпик, эпик — на задачу.
	e, err := s.GetEpic(ctx, t.EpicID)
	if err != nil {
		return err
	}
	e.Tasks = append(e.Tasks, t.TaskID)
	if err := s.SaveEpic(ctx, e); err != nil {
		return err
	}
	// Ф-1: авто-действия при создании задачи (gitflow: фича-ветка от ветки
	// эпика). Хук внедряется сервером; ошибки хука не ломают само сохранение.
	if s.TaskCreatedHook != nil {
		s.TaskCreatedHook(ctx, t)
	}
	return nil
}

// GetTask возвращает задачу по ID.
func (s *Store) GetTask(ctx context.Context, id string) (*Task, error) {
	data, err := s.client.Get(ctx, s.taskKey(id)).Bytes()
	if err == redis.Nil {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var t Task
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// SetTaskInjections заменяет список инъекций задачи. Единственная точка
// записи Task.Injections: список нормализуется и валидируется (вызывающий
// отвечает пользователю 400, а не молча пишет мусор в промпт), после чего
// сохраняется вместе с задачей. Пустой список удаляет все инъекции.
func (s *Store) SetTaskInjections(ctx context.Context, taskID string, injs []Injection) error {
	if len(injs) > 0 {
		if err := ValidateInjections(injs); err != nil {
			return err
		}
	}
	t, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	t.Injections = injs
	return s.SaveTask(ctx, t)
}

// AddTaskInjection добавляет инъекцию к задаче (запись с тем же id
// перезаписывается) и возвращает итоговый список.
func (s *Store) AddTaskInjection(ctx context.Context, taskID string, inj Injection) ([]Injection, error) {
	t, err := s.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	inj.Normalize()
	if err := inj.Validate(); err != nil {
		return nil, err
	}
	out := make([]Injection, 0, len(t.Injections)+1)
	for _, existing := range t.Injections {
		if inj.ID != "" && existing.ID == inj.ID {
			continue
		}
		out = append(out, existing)
	}
	out = append(out, inj)
	if err := s.SetTaskInjections(ctx, taskID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// RemoveTaskInjection удаляет инъекцию задачи по id. Возвращает оставшийся
// список и признак «инъекция была» (вызывающий отвечает 404).
func (s *Store) RemoveTaskInjection(ctx context.Context, taskID, injID string) ([]Injection, bool, error) {
	t, err := s.GetTask(ctx, taskID)
	if err != nil {
		return nil, false, err
	}
	out := make([]Injection, 0, len(t.Injections))
	found := false
	for _, existing := range t.Injections {
		if existing.ID == injID {
			found = true
			continue
		}
		out = append(out, existing)
	}
	if !found {
		return t.Injections, false, nil
	}
	if err := s.SetTaskInjections(ctx, taskID, out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// SetTaskComments заменяет список комментариев задачи.
func (s *Store) SetTaskComments(ctx context.Context, taskID string, cms Comments) error {
	if len(cms) > 0 {
		for i := range cms {
			cms[i].Normalize()
			cms[i].TaskID = taskID
			if err := cms[i].Validate(); err != nil {
				return err
			}
		}
	}
	t, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	t.Comments = cms
	return s.SaveTask(ctx, t)
}

// AddTaskComment добавляет комментарий к задаче (с тем же id перезаписывается).
func (s *Store) AddTaskComment(ctx context.Context, taskID string, cm Comment) (Comments, error) {
	t, err := s.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	cm.Normalize()
	cm.TaskID = taskID
	if err := cm.Validate(); err != nil {
		return nil, err
	}
	out := make(Comments, 0, len(t.Comments)+1)
	for _, existing := range t.Comments {
		if cm.ID != "" && existing.ID == cm.ID {
			continue
		}
		out = append(out, existing)
	}
	out = append(out, cm)
	if err := s.SetTaskComments(ctx, taskID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// RemoveTaskComment удаляет комментарий задачи по id.
func (s *Store) RemoveTaskComment(ctx context.Context, taskID, cmID string) (Comments, bool, error) {
	t, err := s.GetTask(ctx, taskID)
	if err != nil {
		return nil, false, err
	}
	out := make(Comments, 0, len(t.Comments))
	found := false
	for _, existing := range t.Comments {
		if existing.ID == cmID {
			found = true
			continue
		}
		out = append(out, existing)
	}
	if !found {
		return t.Comments, false, nil
	}
	if err := s.SetTaskComments(ctx, taskID, out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// ReopenTask переоткрывает ВЫПОЛНЕННУЮ задачу на доработку: done → infra с
// сохранением прежнего исполнителя (assignee не меняется — дорабатывает тот же
// специалист). Обоснование обязательно: либо уже добавленные необработанные
// комментарии задачи, либо новый комментарий reason.Body. Все использованные
// комментарии помечаются учтёнными (Accounted), поэтому повторно переоткрыть
// задачу тем же комментарием нельзя — иначе доработка запускалась бы по одному
// и тому же поводу бесконечно.
//
// Переход done → infra запрещён общим FSM (терминальный статус), но это
// осознанное действие человека через отдельный эндпоинт (POST .../reopen) с
// обязательным комментарием; поэтому статус ставится напрямую, минуя
// ValidateTransition.
//
// Если эпик задачи уже выполнен, он тоже возвращается в очередь: иначе
// оркестратор не увидит переоткрытую задачу в очереди.
//
// Возвращает обновлённую задачу и число учтённых комментариев.
func (s *Store) ReopenTask(ctx context.Context, taskID string, reason Comment) (*Task, int, error) {
	t, err := s.GetTask(ctx, taskID)
	if err != nil {
		return nil, 0, err
	}
	if t.Status != StatusDone {
		return nil, 0, fmt.Errorf("board: переоткрыть можно только выполненную задачу (текущий статус %q)", t.Status)
	}

	accounted := 0
	if body := strings.TrimSpace(reason.Body); body != "" {
		reason.Body = body
		reason.TaskID = taskID
		if strings.TrimSpace(reason.Type) == "" {
			reason.Type = CommentTypeUser
		}
		if strings.TrimSpace(reason.Author) == "" {
			reason.Author = CommentTypeUser
		}
		reason.Normalize()
		reason.Accounted = true
		if err := reason.Validate(); err != nil {
			return nil, 0, err
		}
		t.Comments = append(t.Comments, reason)
		accounted++
	}
	for i := range t.Comments {
		if t.Comments[i].Accounted {
			continue
		}
		t.Comments[i].Accounted = true
		accounted++
	}
	if accounted == 0 {
		return nil, 0, fmt.Errorf("board: нельзя переоткрыть задачу без комментария")
	}

	if e, err := s.GetEpic(ctx, t.EpicID); err == nil && e.Status == StatusDone {
		e.Status = StatusSystemAnalysis
		if err := s.SaveEpic(ctx, e); err != nil {
			return nil, 0, err
		}
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, 0, err
	}

	t.Status = StatusInfra
	t.PauseReason = ""
	t.LastError = ""
	if err := s.SaveTask(ctx, t); err != nil {
		return nil, 0, err
	}
	return t, accounted, nil
}

// PatchTask читает задачу, применяет mutate к её полям и сохраняет результат.
// Точечное обновление вместо SaveTask вызывающего: State Tracking пишет задачу
// каждый раунд (агент), а статус меняют человек и оркестратор — общая запись
// целого объекта теряла бы поля одного из писателей.
func (s *Store) PatchTask(ctx context.Context, taskID string, mutate func(*Task) error) error {
	if strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("board: task_id обязателен")
	}
	t, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if mutate == nil {
		return nil
	}
	if err := mutate(t); err != nil {
		return err
	}
	if err := s.SaveTask(ctx, t); err != nil {
		return err
	}
	if s.TaskStateHook != nil {
		s.TaskStateHook(ctx, t)
	}
	return nil
}

// SaveTask сохраняет изменения задачи (обновляет и добавляет в индекс).
func (s *Store) SaveTask(ctx context.Context, t *Task) error {
	if t.TaskID == "" {
		return fmt.Errorf("board: task_id обязателен")
	}
	t.ProjectName = s.project
	t.UpdatedAt = now()
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err := s.client.Set(ctx, s.taskKey(t.TaskID), data, s.ttl).Err(); err != nil {
		return err
	}
	return s.client.SAdd(ctx, s.tasksID(), t.TaskID).Err()
}

// ListTasks возвращает все задачи доски, отсортированные по TaskID.
func (s *Store) ListTasks(ctx context.Context) ([]*Task, error) {
	ids, err := s.client.SMembers(ctx, s.tasksID()).Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	tasks := make([]*Task, 0, len(ids))
	for _, id := range ids {
		t, err := s.GetTask(ctx, id)
		if err != nil {
			if err == ErrNotFound {
				continue
			}
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// TasksByEpic возвращает задачи указанного эпика, отсортированные по
// sequence_order (порядок выполнения), затем по TaskID.
func (s *Store) TasksByEpic(ctx context.Context, epicID string) ([]*Task, error) {
	all, err := s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var out []*Task
	for _, t := range all {
		if t.EpicID == epicID {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SequenceOrder.Int() != out[j].SequenceOrder.Int() {
			return out[i].SequenceOrder.Int() < out[j].SequenceOrder.Int()
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out, nil
}

// SetTaskStatus переводит задачу в новый статус (с проверкой перехода).
func (s *Store) SetTaskStatus(ctx context.Context, id string, st Status) error {
	t, err := s.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if err := ValidateTransition(t.Status, st, EntityTypeTask); err != nil {
		return err
	}
	from := t.Status
	// Ф-6 (инцидент FEL-04): переход в done проверяется ДО применения, иначе
	// задача без результата (пустая ветка, чистый worktree) успевает стать
	// выполненной, а откатить её потом нечем — статус терминальный. Гард
	// видит задачу в прежнем статусе.
	if st == StatusDone && s.TaskDoneGuard != nil {
		if err := s.TaskDoneGuard(ctx, t, from); err != nil {
			return err
		}
	}
	t.Status = st
	if err := s.SaveTask(ctx, t); err != nil {
		return err
	}
	if s.TaskStatusChangedHook != nil {
		s.TaskStatusChangedHook(ctx, t, from, st)
	}
	// Ф-2: авто-действия при достижении done (gitflow: ветка задачи → релизная
	// ветка эпика). Хук внедряется сервером; ошибки хука не ломают сам переход.
	if st == StatusInfra && s.TaskInfraHook != nil {
		s.TaskInfraHook(ctx, t, from)
	}
	if st == StatusDone && s.TaskDoneHook != nil {
		s.TaskDoneHook(ctx, t, from)
	}
	return nil
}

// MoveTask переносит задачу в другой эпик: обновляет двусторонние связи
// (задача — новый эпик, списки задач у обоих эпиков). Целевой эпик должен
// существовать.
func (s *Store) MoveTask(ctx context.Context, taskID, toEpicID string) error {
	t, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if _, err := s.GetEpic(ctx, toEpicID); err != nil {
		return err
	}
	if toEpicID == t.EpicID {
		return nil
	}
	fromEpic, err := s.GetEpic(ctx, t.EpicID)
	if err == nil {
		fromEpic.Tasks = removeString(fromEpic.Tasks, taskID)
		if err := s.SaveEpic(ctx, fromEpic); err != nil {
			return err
		}
	}
	t.EpicID = toEpicID
	if err := s.SaveTask(ctx, t); err != nil {
		return err
	}
	toEpic, err := s.GetEpic(ctx, toEpicID)
	if err != nil {
		return err
	}
	toEpic.Tasks = append(toEpic.Tasks, taskID)
	return s.SaveEpic(ctx, toEpic)
}

// DeleteTask удаляет задачу с доски (вместе с индексами).
// Можно удалить: задачи в начальных статусах (draft, infra) — ещё не взяты
// в работу, и задачи в статусе done — завершенную работу можно убрать.
// Нельзя удалить: задачи в test_cases/development — специалист в работе,
// или cancelled — отменённая задача может иметь зависящие записи.
// Засависившие от неё задачи — ошибка (иначе на доске останутся битые связи).
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	t, err := s.GetTask(ctx, id)
	if err != nil {
		return err
	}
	// Нельзя удалить задачи, которые специалист реально начал делать,
	// или отменённые (могут быть зависящие записи).
	if t.Status == StatusTestCases || t.Status == StatusDevelopment || t.Status == StatusCancelled {
		return fmt.Errorf("board: задачу %q нельзя удалить (статус %s: специалист начал работу или задача отменена)", id, t.Status)
	}
	if err := s.checkNoDependents(ctx, "task:"+id); err != nil {
		return err
	}

	// Убираем ID из списка задач эпика и разрываем двустороннюю связь.
	e, err := s.GetEpic(ctx, t.EpicID)
	if err == nil {
		e.Tasks = removeString(e.Tasks, id)
		if err := s.SaveEpic(ctx, e); err != nil {
			return err
		}
	}

	if err := s.client.Del(ctx, s.taskKey(id)).Err(); err != nil {
		return err
	}
	return s.client.SRem(ctx, s.tasksID(), id).Err()
}
