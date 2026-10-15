package board

import (
	"context"
	"fmt"
	"time"

	"ai/domain"
	"github.com/redis/go-redis/v9"
)

// ErrNotFound — сущность доски или метаданные не найдены.
var ErrNotFound = domain.ErrNotFound

// ErrExists — запись с таким ID уже есть на доске.
var ErrExists = domain.ErrExists

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
	// Addr — адрес Redis вида "host:port". По умолчанию "localhost:56379".
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
	client       *redis.Client
	project      string
	ttl          time.Duration
	repositories []string

	// TaskDoneHook — опциональный обратный вызов после перевода задачи в
	// StatusDone (Ф-2). Вызывается из SetTaskStatus вне блокировок, после
	// успешного сохранения (входной task — сохранённый). Доска не знает про
	// git/workflow — назначение: сервер внедряет сюда авто-вливание ветки
	// задачи в релизную ветку эпика при достижении done. nil — хука нет
	// (значение по умолчанию, поведение прежних вызовов не меняется).
	TaskDoneHook func(ctx context.Context, task *Task, from Status)

	// TaskDoneGuard — опциональная ПРОВЕРКА до перевода в StatusDone: вернуть
	// ошибку и не менять статус. Живой повод (mytrip, FEL-04): задача без
	// единого коммита и без правок в worktree получила done от fallback'а
	// оркестратора («агент не сменил статус») — в MR уехала пустая ветка, а
	// доска показала выполненную работу. Назначение: сервер внедряет сюда
	// проверку «в ветке задачи есть свои коммиты ИЛИ worktree непуст».
	// Ошибка гарда возвращается вызывающему (UI/API/агент), статус не
	// меняется. nil — гарда нет (поведение прежних вызовов не меняется).
	TaskDoneGuard func(ctx context.Context, task *Task, from Status) error

	// TaskInProgressHook — опциональный обратный вызов при переводе задачи в
	// StatusInProgress (Ф-3). Вызывается из SetTaskStatus вне блокировок, после
	// успешного сохранения. Назначение: сервер внедряет сюда создание worktree
	// ветки задачи, в который специалист получает OutputDir. nil — хука нет.
	TaskInProgressHook func(ctx context.Context, task *Task, from Status)

	// EpicCreatedHook — опциональный обратный вызов после создания эпика (Ф-1).
	// Вызывается из CreateEpic вне блокировок, после успешного сохранения
	// (входной epic — сохранённый). Назначение: сервер внедряет сюда авто-создание
	// релизной ветки эпика у git-проектов. nil — хука нет.
	EpicCreatedHook func(ctx context.Context, epic *Epic)

	// TaskCreatedHook — опциональный обратный вызов после создания задачи (Ф-1).
	// Вызывается из CreateTask вне блокировок, после успешного сохранения.
	// Назначение: сервер внедряет сюда авто-создание фича-ветки задачи (база —
	// релизная ветка её эпика). nil — хука нет.
	TaskCreatedHook func(ctx context.Context, task *Task)

	// EpicDoneHook — опциональный обратный вызов при переводе эпика в StatusDone
	// (Ф-4). Вызывается из SetEpicStatus вне блокировок, после успешного
	// сохранения. Назначение: сервер внедряет сюда авто-синхрон релизной ветки
	// эпика с main (merge + авто-резолв конфликтов). nil — хука нет.
	EpicDoneHook func(ctx context.Context, epic *Epic, from Status)

	// TokenEstimate — необязательный источник прогноза расхода токенов для
	// новых единиц доски (Ф-5 PLAN-2026-09-19-done-epic-task-token.md).
	// Вызывается в CreateEpic/CreateTask, если оценка ещё не задана (0);
	// задача/эпик с явной оценкой не переоцениваются. Возвращаемое значение
	// <= 0 — оценки нет (история мала/прогноз не считается). nil — доска
	// просто не проставляет оценки (консольный режим).
	TokenEstimate func(ctx context.Context, e *Epic, t *Task) int64

	// TaskStateHook — опциональный обратный вызов после точечного обновления
	// задачи через PatchTask (Ф-6, State Tracking). Назначение: сервер
	// публикует обновлённый снимок доски, иначе «чем занят агент» было бы
	// видно только после смены статуса. nil — хука нет.
	TaskStateHook func(ctx context.Context, t *Task)
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

func (s *Store) bugKey(id string) string { return s.key("bug:" + id) }

func (s *Store) metaKey() string { return s.key("meta") }

// NewStore создаёт хранилище доски и проверяет доступность Redis.
func NewStore(ctx context.Context, cfg StoreConfig) (*Store, error) {
	addr := cfg.Addr
	if addr == "" {
		addr = "localhost:56379"
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
		addr = "localhost:56379"
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

// SetRepositories sets the participating repository names stored with new
// epics and tasks. The primary board project is always included.
func (s *Store) SetRepositories(repositories []string) {
	s.repositories = append([]string(nil), repositories...)
	if len(s.repositories) == 0 {
		s.repositories = []string{s.project}
	}
}

func (s *Store) applyRepositories(dst *[]string) {
	if len(*dst) == 0 {
		if len(s.repositories) > 0 {
			*dst = append([]string(nil), s.repositories...)
		} else {
			*dst = []string{s.project}
		}
	}
}

// setTTL применяет TTL к ключу (если он задан).
func (s *Store) setTTL(ctx context.Context, key string) {
	if s.ttl > 0 {
		s.client.Expire(ctx, key, s.ttl)
	}
}

// now возвращает текущее время в RFC3339.
func now() string { return time.Now().UTC().Format(time.RFC3339) }

// --- Эпики ---

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
