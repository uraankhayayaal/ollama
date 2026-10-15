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

// CreateEpic добавляет эпик на доску. ID эпика обязателен и должен быть
// уникальным в пределах проекта. Возвращает ошибку, если эпик уже существует.
func (s *Store) CreateEpic(ctx context.Context, e *Epic) error {
	if e.TaskID == "" {
		return fmt.Errorf("board: ID эпика обязателен")
	}
	e.ProjectName = s.project
	s.applyRepositories(&e.Repositories)
	e.Status = FirstNonZeroStatus(e.Status, StatusNew)
	e.CreatedAt = now()
	e.UpdatedAt = e.CreatedAt
	// Ф-5: прогноз расхода токенов по истории завершённых единиц (если задан).
	if e.TokenEstimate == 0 && s.TokenEstimate != nil {
		if v := s.TokenEstimate(ctx, e, nil); v > 0 {
			e.TokenEstimate = v
		}
	}

	if ok, err := s.client.SIsMember(ctx, s.epicsID(), e.TaskID).Result(); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: эпик %q", ErrExists, e.TaskID)
	}

	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.setTTL(ctx, s.epicKey(e.TaskID))
	if err := s.client.Set(ctx, s.epicKey(e.TaskID), data, s.ttl).Err(); err != nil {
		return err
	}
	if err := s.client.SAdd(ctx, s.epicsID(), e.TaskID).Err(); err != nil {
		return err
	}
	// Ф-1: авто-действия при создании эпика (gitflow: релизная ветка эпика).
	// Хук внедряется сервером; ошибки хука не ломают само сохранение.
	if s.EpicCreatedHook != nil {
		s.EpicCreatedHook(ctx, e)
	}
	return nil
}

// GetEpic возвращает эпик по ID.
func (s *Store) GetEpic(ctx context.Context, id string) (*Epic, error) {
	data, err := s.client.Get(ctx, s.epicKey(id)).Bytes()
	if err == redis.Nil {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var e Epic
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// SaveEpic сохраняет изменения эпика (обновляет и добавляет в индекс).
func (s *Store) SaveEpic(ctx context.Context, e *Epic) error {
	if e.TaskID == "" {
		return fmt.Errorf("board: ID эпика обязателен")
	}
	e.ProjectName = s.project
	e.UpdatedAt = now()
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := s.client.Set(ctx, s.epicKey(e.TaskID), data, s.ttl).Err(); err != nil {
		return err
	}
	return s.client.SAdd(ctx, s.epicsID(), e.TaskID).Err()
}

// ListEpics возвращает все эпики доски, отсортированные по ID.
func (s *Store) ListEpics(ctx context.Context) ([]*Epic, error) {
	ids, err := s.client.SMembers(ctx, s.epicsID()).Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	epics := make([]*Epic, 0, len(ids))
	for _, id := range ids {
		e, err := s.GetEpic(ctx, id)
		if err != nil {
			if err == ErrNotFound {
				continue
			}
			return nil, err
		}
		epics = append(epics, e)
	}
	return epics, nil
}

// ReorderEpics задаёт приоритет эпиков по порядку из Web UI: первый в списке
// получает наибольший Priority, последний — наименьший. Порядок из UI
// присваивается сквозным счётом (n-i), поэтому относительный порядок внутри
// каждой фазы сохраняется, а межфазовые переносы строки эффективного порядка
// не меняют (фаза первична, см. EpicLess). Ревизию эпиков НЕ увеличиваем:
// приоритет — не содержание, повторная декомпозиция лидом не нужна.
//
// Неизвестный ID — ошибка (ничего не сохраняем поверх частичного результата
// молча): раньше сохранённые эпики к этому моменту уже обновлены, но клиент
// получает явную ошибку и перечитает доску. Пропущенные в order эпики не
// трогаются (сохраняют прежний приоритет).
func (s *Store) ReorderEpics(ctx context.Context, order []string) (int, error) {
	n := len(order)
	updated := 0
	for i, id := range order {
		e, err := s.GetEpic(ctx, id)
		if err != nil {
			return updated, fmt.Errorf("эпик %s: %w", id, err)
		}
		prio := n - i
		if e.Priority == prio {
			continue
		}
		e.Priority = prio
		if err := s.SaveEpic(ctx, e); err != nil {
			return updated, fmt.Errorf("эпик %s: %w", id, err)
		}
		updated++
	}
	return updated, nil
}

// SetEpicStatus переводит эпик в новый статус (с проверкой перехода). «Помощь
// человека» (human_help), возобновление (ready/in_progress из human_help) и
// отмена (cancelled) каскадно переводят задачи эпика, следуя за родителем:
// не взятые в работу задачи «останавливаются»/«отменяются», при выходе —
// возвращаются в исходный статус (ResumeStatus). Задачи «в работе»/«на
// тестирование» не трогаются: их доводит текущий раунд оркестратора.
func (s *Store) SetEpicStatus(ctx context.Context, id string, st Status) error {
	e, err := s.GetEpic(ctx, id)
	if err != nil {
		return err
	}
	if err := ValidateTransition(e.Status, st); err != nil {
		return err
	}
	from := e.Status
	e.Status = st
	if err := s.SaveEpic(ctx, e); err != nil {
		return err
	}
	// Каскад статусов задач эпика (без FSM-хуков — прямое сохранение):
	// трансляция остановки/возобновления/отмены родителя на его задачи.
	if st == StatusHumanHelp && from != StatusHumanHelp {
		if err := s.pauseEpicTasks(ctx, id); err != nil {
			return err
		}
	}
	if (st == StatusReady || st == StatusInProgress) && from == StatusHumanHelp {
		if err := s.resumeEpicTasks(ctx, id); err != nil {
			return err
		}
	}
	if st == StatusCancelled && from != StatusCancelled {
		if err := s.setEpicTasksStatus(ctx, id, []Status{StatusNew, StatusAnalysis, StatusReady, StatusHumanHelp}, StatusCancelled); err != nil {
			return err
		}
	}
	// Ф-4: авто-синхрон релизной ветки эпика с main при достижении done.
	// Хук внедряется сервером; ошибки хука не ломают сам переход.
	if st == StatusDone && s.EpicDoneHook != nil {
		s.EpicDoneHook(ctx, e, from)
	}
	return nil
}

// setEpicTasksStatus переводит задачи эпика из from статусов в to напрямую
// (обход FSM — статусы задач следуют за каскадом родителя). Задачи с другими
// статусами (в работе/на тестировании/выполнены) не трогаются.
func (s *Store) setEpicTasksStatus(ctx context.Context, epicID string, from []Status, to Status) error {
	tasks, err := s.TasksByEpic(ctx, epicID)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		prev := t.Status
		match := false
		for _, f := range from {
			if prev == f {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		t.Status = to
		t.ResumeStatus = ""
		if err := s.SaveTask(ctx, t); err != nil {
			return fmt.Errorf("задача %s: %s -> %s: %w", t.TaskID, prev, to, err)
		}
	}
	return nil
}

// pauseEpicTasks останавливает каскадом задачи эпика, которые ещё не взял в
// работу специалист (new/analysis/ready), запоминая исходный статус в
// ResumeStatus — возобновление вернёт задачу на прежнее место цепочки
// (зависимости и фазовые гейты оркестратор проверит заново). Задачи «в
// работе»/«на тестирование» не трогаются: их доводит текущий раунд
// оркестратора, иначе агент не смог бы закрыть задачу.
func (s *Store) pauseEpicTasks(ctx context.Context, epicID string) error {
	tasks, err := s.TasksByEpic(ctx, epicID)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		switch t.Status {
		case StatusNew, StatusAnalysis, StatusReady:
		default:
			continue
		}
		t.ResumeStatus = t.Status
		t.Status = StatusHumanHelp
		if err := s.SaveTask(ctx, t); err != nil {
			return fmt.Errorf("задача %s: помощь человека: %w", t.TaskID, err)
		}
	}
	return nil
}

// resumeEpicTasks возвращает остановленные каскадом задачи эпика в статус,
// из которого они были остановлены (ResumeStatus). Задачи БЕЗ ResumeStatus —
// «помощь человека» понадобилась им самим (эскалация, форсмажор) — каскад не
// трогает: возврат с human_help делается только вручную.
func (s *Store) resumeEpicTasks(ctx context.Context, epicID string) error {
	tasks, err := s.TasksByEpic(ctx, epicID)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Status != StatusHumanHelp {
			continue
		}
		if t.ResumeStatus == "" {
			continue
		}
		t.Status = t.ResumeStatus
		t.ResumeStatus = ""
		if err := s.SaveTask(ctx, t); err != nil {
			return fmt.Errorf("задача %s: возобновление: %w", t.TaskID, err)
		}
	}
	return nil
}

// --- Задачи ---

// DeleteEpic удаляет эпик вместе с его задачами. Удалять можно только эпики,
// задачи которых ещё не взяты в работу специалистами. Зависимости извне
// эпика — ошибка.
func (s *Store) DeleteEpic(ctx context.Context, id string) error {
	e, err := s.GetEpic(ctx, id)
	if err != nil {
		return err
	}
	if err := s.checkNoDependents(ctx, "epic:"+id); err != nil {
		return err
	}

	for _, taskID := range e.Tasks {
		// Каскадное удаление задач эпика. Задачи в работе удалять нельзя:
		// ошибка прерывает всё удаление эпика.
		if err := s.DeleteTask(ctx, taskID); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return fmt.Errorf("board: эпик %q: удаление задачи %q: %w", id, taskID, err)
		}
	}

	if err := s.client.Del(ctx, s.epicKey(id)).Err(); err != nil {
		return err
	}
	return s.client.SRem(ctx, s.epicsID(), id).Err()
}

// checkNoDependents возвращает ошибку, если на доске есть задачи или эпики,
// зависящие от указанной записи (ref вида "task:<id>" или "epic:<id>").
// Зависимости хранятся голыми ID (без префикса), поэтому сравниваем оба.
func (s *Store) checkNoDependents(ctx context.Context, ref string) error {
	target := strings.TrimPrefix(strings.TrimPrefix(ref, "task:"), "epic:")
	depends := func(list []string) bool {
		for _, dep := range list {
			if dep == target {
				return true
			}
		}
		return false
	}

	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if depends(t.Dependencies) {
			return &ErrDependency{Ref: t.TaskID + " (зависит от " + ref + ")"}
		}
	}
	epics, err := s.ListEpics(ctx)
	if err != nil {
		return err
	}
	for _, e2 := range epics {
		if depends(e2.Dependencies) {
			return &ErrDependency{Ref: e2.TaskID + " (зависит от " + ref + ")"}
		}
	}
	return nil
}
