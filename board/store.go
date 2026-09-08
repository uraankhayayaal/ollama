package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound — сущность доски или метаданные не найдены.
var ErrNotFound = errors.New("запись на доске не найдена")

// ErrExists — запись с таким ID уже есть на доске.
var ErrExists = errors.New("запись с таким ID уже есть на доске")

// ErrDependency — нарушение связей доски (несуществующий эпик/задача в
// зависимости или ссылке).
type ErrDependency struct {
	Ref string
}

func (e *ErrDependency) Error() string {
	return "неработающая связь на доске: " + e.Ref
}

// StoreConfig — параметры подключения Redis-хранилища доски.
type StoreConfig struct {
	// Addr — адрес Redis вида "host:port". По умолчанию "localhost:6379".
	Addr string
	// Password — пароль Redis (пусто — без пароля).
	Password string
	// DB — номер базы Redis.
	DB int
	// Project — имя проекта, для которого ведётся доска (префикс ключей).
	Project string
	// TTL — время жизни ключей доски. 0 — без истечения.
	TTL time.Duration
}

// Store — Redis-хранилище общей Kanban-доски проекта: эпики, задачи,
// метаданные и статусы. Ключи сгруппированы под префиксом board:<project>.
type Store struct {
	client  *redis.Client
	project string
	ttl     time.Duration
}

// key возвращает полный ключ Redis для относительного имени.
func (s *Store) key(name string) string {
	return "board:" + s.project + ":" + name
}

// epicsID — set-ключ с ID эпиков проекта.
func (s *Store) epicsID() string { return s.key("epics") }

// tasksID — set-ключ с ID задач проекта.
func (s *Store) tasksID() string { return s.key("tasks") }

// bugsID — set-ключ с ID багрепортов проекта.
func (s *Store) bugsID() string { return s.key("bugs") }

func (s *Store) epicKey(id string) string { return s.key("epic:" + id) }
func (s *Store) taskKey(id string) string { return s.key("task:" + id) }
func (s *Store) bugKey(id string) string  { return s.key("bug:" + id) }
func (s *Store) metaKey() string          { return s.key("meta") }

// NewStore создаёт хранилище доски и проверяет доступность Redis.
func NewStore(ctx context.Context, cfg StoreConfig) (*Store, error) {
	addr := cfg.Addr
	if addr == "" {
		addr = "localhost:6379"
	}
	if cfg.Project == "" {
		return nil, fmt.Errorf("board: имя проекта обязательно для хранилища доски")
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("board: redis недоступен (%s): %w", addr, err)
	}
	return &Store{client: client, project: cfg.Project, ttl: cfg.TTL}, nil
}

// NewStoreNoCheck создаёт хранилище без проверки соединения (для тестов).
func NewStoreNoCheck(cfg StoreConfig) *Store {
	addr := cfg.Addr
	if addr == "" {
		addr = "localhost:6379"
	}
	return &Store{
		client:  redis.NewClient(&redis.Options{Addr: addr, Password: cfg.Password, DB: cfg.DB}),
		project: cfg.Project,
		ttl:     cfg.TTL,
	}
}

// Close закрывает соединение с Redis.
func (s *Store) Close() error { return s.client.Close() }

// Client возвращает Redis-клиент (для тестов и низкоуровневых операций).
func (s *Store) Client() *redis.Client { return s.client }

// Project возвращает имя проекта, для которого ведётся доска.
func (s *Store) Project() string { return s.project }

// setTTL применяет TTL к ключу (если он задан).
func (s *Store) setTTL(ctx context.Context, key string) {
	if s.ttl > 0 {
		s.client.Expire(ctx, key, s.ttl)
	}
}

// now возвращает текущее время в RFC3339.
func now() string { return time.Now().UTC().Format(time.RFC3339) }

// --- Эпики ---

// CreateEpic добавляет эпик на доску. ID эпика обязателен и должен быть
// уникальным в пределах проекта. Возвращает ошибку, если эпик уже существует.
func (s *Store) CreateEpic(ctx context.Context, e *Epic) error {
	if e.TaskID == "" {
		return fmt.Errorf("board: ID эпика обязателен")
	}
	e.ProjectName = s.project
	e.Status = FirstNonZeroStatus(e.Status, StatusNew)
	e.CreatedAt = now()
	e.UpdatedAt = e.CreatedAt

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
	return s.client.SAdd(ctx, s.epicsID(), e.TaskID).Err()
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

// SetEpicStatus переводит эпик в новый статус (с проверкой перехода).
func (s *Store) SetEpicStatus(ctx context.Context, id string, st Status) error {
	e, err := s.GetEpic(ctx, id)
	if err != nil {
		return err
	}
	if err := ValidateTransition(e.Status, st); err != nil {
		return err
	}
	e.Status = st
	return s.SaveEpic(ctx, e)
}

// --- Задачи ---

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

	t.Status = FirstNonZeroStatus(t.Status, StatusNew)
	t.CreatedAt = now()
	t.UpdatedAt = t.CreatedAt

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
	return s.SaveEpic(ctx, e)
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
	if err := ValidateTransition(t.Status, st); err != nil {
		return err
	}
	t.Status = st
	return s.SaveTask(ctx, t)
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

// DeleteTask удаляет задачу с доски (вместе с индексами). Удалять можно
// только задачи, которые ещё не взял в работу специалист (ready/new/analysis).
// Засависившие от неё задачи — ошибка (иначе на доске останутся битые связи).
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	t, err := s.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if t.Status == StatusInProgress || t.Status == StatusDone || t.Status == StatusCancelled {
		return fmt.Errorf("board: задачу %q нельзя удалить (статус %s: специалист уже начал работу или работа завершена)", id, t.Status)
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

// removeString удаляет первое вхождение v из списка.
func removeString(list []string, v string) []string {
	for i, x := range list {
		if x == v {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

// --- Багрепорты ---

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

// StatusCounts — распределение записей по статусам.
type StatusCounts struct {
	Total int
	By    map[Status]int
}

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
	// Открытые багрепорты (не прошедшие ревью до терминального статуса)
	// блокируют решение задачи пользователя.
	bugs, err := s.ListBugReports(ctx)
	if err != nil {
		return false, err
	}
	for _, b := range bugs {
		if !b.Status.Terminal() {
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

// FirstNonZeroBugStatus возвращает первый ненулевой (непустой) статус багрепорта.
func FirstNonZeroBugStatus(values ...BugStatus) BugStatus {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
