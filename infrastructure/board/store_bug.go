package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/redis/go-redis/v9"
)

// CreateBugReport добавляет багрепорт на доску. ID обязателен и уникален.
// Проблема привязывается к эпику (EpicID), а при наличии и к задаче-источнику
// (TaskID) — оба должны существовать на доске.
func (s *Store) CreateBugReport(ctx context.Context, r *BugReport) error {
	if r.BugID == "" {
		return fmt.Errorf("board: bug_id обязателен")
	}
	if r.EpicID == "" {
		return fmt.Errorf("board: багрепорт %q должен ссылаться на эпик (epic_id)", r.BugID)
	}
	if _, err := s.GetEpic(ctx, r.EpicID); err != nil {
		return &ErrDependency{Ref: "epic:" + r.EpicID}
	}
	if r.TaskID != "" {
		if _, err := s.GetTask(ctx, r.TaskID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return &ErrDependency{Ref: "task:" + r.TaskID}
			}
			return err
		}
	}
	if ok, err := s.client.SIsMember(ctx, s.bugsID(), r.BugID).Result(); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: багрепорт %q", ErrExists, r.BugID)
	}

	r.ProjectName = s.project
	r.Status = FirstNonZeroBugStatus(r.Status, BugStatusNew)
	r.CreatedAt = now()
	r.UpdatedAt = r.CreatedAt

	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := s.client.Set(ctx, s.bugKey(r.BugID), data, s.ttl).Err(); err != nil {
		return err
	}
	return s.client.SAdd(ctx, s.bugsID(), r.BugID).Err()
}

// GetBugReport возвращает багрепорт по ID.
func (s *Store) GetBugReport(ctx context.Context, id string) (*BugReport, error) {
	data, err := s.client.Get(ctx, s.bugKey(id)).Bytes()
	if err == redis.Nil {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var r BugReport
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// SaveBugReport сохраняет изменения багрепорта.
func (s *Store) SaveBugReport(ctx context.Context, r *BugReport) error {
	if r.BugID == "" {
		return fmt.Errorf("board: bug_id обязателен")
	}
	r.ProjectName = s.project
	r.UpdatedAt = now()
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := s.client.Set(ctx, s.bugKey(r.BugID), data, s.ttl).Err(); err != nil {
		return err
	}
	return s.client.SAdd(ctx, s.bugsID(), r.BugID).Err()
}

// ListBugReports возвращает все багрепорты доски, отсортированные по BugID.
func (s *Store) ListBugReports(ctx context.Context) ([]*BugReport, error) {
	ids, err := s.client.SMembers(ctx, s.bugsID()).Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	out := make([]*BugReport, 0, len(ids))
	for _, id := range ids {
		r, err := s.GetBugReport(ctx, id)
		if err != nil {
			if err == ErrNotFound {
				continue
			}
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// SetBugStatus переводит багрепорт в новый статус (с проверкой перехода).
func (s *Store) SetBugStatus(ctx context.Context, id string, st BugStatus) error {
	r, err := s.GetBugReport(ctx, id)
	if err != nil {
		return err
	}
	if err := ValidateBugTransition(r.Status, st); err != nil {
		return err
	}
	r.Status = st
	return s.SaveBugReport(ctx, r)
}

// MarkBugsFixedForEpic закрывает багрепорты «требует исправления» как
// «исправлен», если их эпик исправления (FixEpicID) уже выполнен.
func (s *Store) MarkBugsFixedForEpic(ctx context.Context, epicID string) (int, error) {
	bugs, err := s.ListBugReports(ctx)
	if err != nil {
		return 0, err
	}
	fixed := 0
	for _, r := range bugs {
		if r.FixEpicID != epicID || r.Status == BugStatusFixed {
			continue
		}
		if err := s.SetBugStatus(ctx, r.BugID, BugStatusFixed); err != nil {
			continue
		}
		fixed++
	}
	return fixed, nil
}
