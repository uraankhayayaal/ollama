package board

import (
	"context"
	"encoding/json"

	"ai/domain"
	"github.com/redis/go-redis/v9"
)

// referenceExists проверяет, существует ли на доске запись с указанным ID
// (задача или эпик).
func (s *Store) referenceExists(ctx context.Context, id string) error {
	for _, key := range []string{s.taskKey(id), s.epicKey(id)} {
		if ok, err := s.client.Exists(ctx, key).Result(); err != nil {
			return err
		} else if ok == 1 {
			return nil
		}
	}
	return ErrNotFound
}

// ReferenceExists сообщает, существует ли на доске запись с указанным ID
// (задача или эпик). Экспорт для инструментов доски (tools.Board*).
func (s *Store) ReferenceExists(ctx context.Context, id string) error {
	return s.referenceExists(ctx, id)
}

// --- Метаданные ---

// GetMeta возвращает метаданные доски (исходная задача пользователя, статус).
func (s *Store) GetMeta(ctx context.Context) (*Meta, error) {
	data, err := s.client.Get(ctx, s.metaKey()).Bytes()
	if err == redis.Nil {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// SaveMeta сохраняет метаданные доски.
func (s *Store) SaveMeta(ctx context.Context, m *Meta) error {
	m.ProjectName = s.project
	m.UpdatedAt = now()
	if m.CreatedAt == "" {
		m.CreatedAt = m.UpdatedAt
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.metaKey(), data, s.ttl).Err()
}

// --- Сводки ---

// StatusCounts — распределение записей по статусам. Тип принадлежит домену
// (domain.StatusCounts), здесь — алиас.
type StatusCounts = domain.StatusCounts

// EpicCounts возвращает распределение эпиков по статусам.
func (s *Store) EpicCounts(ctx context.Context) (*StatusCounts, error) {
	epics, err := s.ListEpics(ctx)
	if err != nil {
		return nil, err
	}
	c := &StatusCounts{By: map[Status]int{}}
	for _, e := range epics {
		c.By[e.Status]++
	}
	c.Total = len(epics)
	return c, nil
}

// TaskCounts возвращает распределение задач по статусам.
func (s *Store) TaskCounts(ctx context.Context) (*StatusCounts, error) {
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	c := &StatusCounts{By: map[Status]int{}}
	for _, t := range tasks {
		c.By[t.Status]++
	}
	c.Total = len(tasks)
	return c, nil
}

// AllDone сообщает, решена ли задача пользователя: все эпики и все задачи
// доски успешно выполнены (StatusDone). Отменённая запись — решение НЕ
// считается успешным.
func (s *Store) AllDone(ctx context.Context) (bool, error) {
	epics, err := s.ListEpics(ctx)
	if err != nil {
		return false, err
	}
	for _, e := range epics {
		if e.Status != StatusDone {
			return false, nil
		}
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tasks {
		if t.Status != StatusDone {
			return false, nil
		}
	}
	// Пустая доска (нет ни эпиков, ни задач) не считается решённой.
	if len(epics) == 0 && len(tasks) == 0 {
		return false, nil
	}
	return true, nil
}

// FirstNonZeroStatus возвращает первый ненулевой (непустой) статус.
func FirstNonZeroStatus(values ...Status) Status {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
