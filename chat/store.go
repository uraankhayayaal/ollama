// Package chat — хранилище диалога Web UI поверх Redis. История сообщений —
// Redis Streams (chat:<project>), live-события — pub/sub (events:<project>).
// Каждое сообщение Append попадает и в стрим (персистентно, читается XREVRANGE),
// и в канал (мгновенно доставляется подписчикам сокета).
package chat

import (
	"ai/domain"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Данные диалога (Role/Message/Ask*) определены в домене (domain.ChatRole/
// ChatMessage/Ask*) — общие для application-слоя и этого хранилища (Этап 8,
// H5b). Здесь они алиасы: имена chat.* сохранены для совместимости.
type (
	Role        = domain.ChatRole
	Message     = domain.ChatMessage
	AskKind     = domain.AskKind
	Ask         = domain.Ask
	AskQuestion = domain.AskQuestion
	AskOption   = domain.AskOption
	AskAnswer   = domain.AskAnswer
)

const (
	RoleUser      = domain.ChatRoleUser
	RoleAssistant = domain.ChatRoleAssistant
	RoleTool      = domain.ChatRoleTool
	RoleSystem    = domain.ChatRoleSystem
	RoleStatus    = domain.ChatRoleStatus
	RoleBoard     = domain.ChatRoleBoard
	RoleAsk       = domain.ChatRoleAsk

	AskSingle = domain.AskSingle
	AskMulti  = domain.AskMulti
)

// StoreConfig — параметры подключения Redis-хранилища диалога.
type StoreConfig struct {
	// Addr — адрес Redis вида "host:port". По умолчанию "localhost:56379".
	Addr string
	// Password — пароль Redis (пусто — без пароля).
	Password string
	// DB — номер базы Redis.
	DB int
	// Project — имя проекта, для которого ведётся диалог (префикс ключей).
	Project string
}

// Store — Redis-хранилище диалога: стрим истории + канал live-событий.
type Store struct {
	client  *redis.Client
	project string
	stream  string
	channel string
}

// maxStreamLen — максимальное число записей истории (старые срезаются).
const maxStreamLen = 10000

// NewStore создаёт хранилище диалога и проверяет доступность Redis.
func NewStore(ctx context.Context, cfg StoreConfig) (*Store, error) {
	if cfg.Project == "" {
		return nil, fmt.Errorf("chat: имя проекта обязательно для хранилища диалога")
	}
	s := NewStoreNoCheck(cfg)
	if err := s.client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("chat: redis недоступен (%s): %w", s.client.Options().Addr, err)
	}
	return s, nil
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
		stream:  "chat:" + cfg.Project,
		channel: "events:" + cfg.Project,
	}
}

// Close закрывает соединение с Redis.
func (s *Store) Close() error { return s.client.Close() }

// Client возвращает Redis-клиент (для низкоуровневых операций и тестов).
func (s *Store) Client() *redis.Client { return s.client }

// Project возвращает имя проекта, для которого ведётся диалог.
func (s *Store) Project() string { return s.project }

// StreamName возвращает имя Redis Stream с историей диалога.
func (s *Store) StreamName() string { return s.stream }

// Append публикует сообщение: добавляет в стрим истории и рассылает в канал
// live-событий. Возвращает ID записи стрима.
func (s *Store) Append(ctx context.Context, m Message) (string, error) {
	if m.Time.IsZero() {
		m.Time = time.Now().UTC()
	}
	fields := messageFields(m)
	id, err := s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: s.stream,
		MaxLen: maxStreamLen,
		Approx: true,
		Values: fields,
	}).Result()
	if err != nil {
		return "", fmt.Errorf("chat: запись в стрим %s: %w", s.stream, err)
	}
	m.ID = id
	payload, err := jsonMarshal(m)
	if err != nil {
		return id, err
	}
	// live-рассылка не критична: если канал недоступен, история в стриме остаётся.
	_ = s.client.Publish(ctx, s.channel, payload).Err()
	return id, nil
}

// History возвращает последние limit сообщений диалога в хронологическом
// порядке (старые → новые). Запись берется из стрима XREVRANGE.
func (s *Store) History(ctx context.Context, limit int64) ([]Message, error) {
	if limit <= 0 {
		limit = 200
	}
	entries, err := s.client.XRevRangeN(ctx, s.stream, "+", "-", limit).Result()
	if err != nil {
		return nil, fmt.Errorf("chat: чтение истории %s: %w", s.stream, err)
	}
	// Разворачиваем в хронологический порядок.
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })

	out := make([]Message, 0, len(entries))
	for _, en := range entries {
		out = append(out, messageFromEntry(en))
	}
	return out, nil
}

// Clear стирает историю диалога целиком («кофе-брейк»): удаляет ключ стрима,
// чтобы UI и модель общались с чистого листа. Канал live-событий не трогается,
// новые сообщения продолжают ходить как обычно.
func (s *Store) Clear(ctx context.Context) error {
	if err := s.client.Del(ctx, s.stream).Err(); err != nil {
		return fmt.Errorf("chat: очистка стрима %s: %w", s.stream, err)
	}
	return nil
}

// Subscribe возвращает подписку на канал live-событий диалога. Вызывающий
// код циклом получает *redis.Message (см. redis.PubSub.ReceiveMessage).
func (s *Store) Subscribe(ctx context.Context) (*redis.PubSub, error) {
	ps := s.client.Subscribe(ctx, s.channel)
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, fmt.Errorf("chat: подписка на канал %s: %w", s.channel, err)
	}
	return ps, nil
}

// messageFields разворачивает Message в поля записи Redis Stream. Свободная
// функция (а не метод): Message — алиас доменного типа, методы на нём
// определить нельзя.
func messageFields(m Message) map[string]any {
	out := map[string]any{
		"role":    string(m.Role),
		"content": m.Content,
		"time":    m.Time.UnixMilli(),
	}
	if m.Agent != "" {
		out["agent"] = m.Agent
	}
	if m.Tool != "" {
		out["tool"] = m.Tool
	}
	if m.OK != nil {
		out["ok"] = *m.OK
	}
	if m.Ask != nil {
		if b, err := json.Marshal(m.Ask); err == nil {
			out["ask"] = string(b)
		}
	}
	return out
}

// jsonMarshal — локальная обёртка над json.Marshal для сокращения имён.
func jsonMarshal(m Message) ([]byte, error) { return json.Marshal(m) }

// fieldString возвращает строковое значение поля записи стрима, "" — если
// поле отсутствует (важно: fmt.Sprint(nil) даёт "<nil>", а не пустую строку).
func fieldString(values map[string]any, key string) string {
	if v, ok := values[key]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

// messageFromEntry собирает Message из записи Redis Stream.
func messageFromEntry(en redis.XMessage) Message {
	var ok *bool
	if raw, exists := en.Values["ok"]; exists {
		v, err := strconv.ParseBool(fmt.Sprint(raw))
		if err == nil {
			ok = &v
		}
	}
	var t time.Time
	if raw, exists := en.Values["time"]; exists {
		if ms, err := strconv.ParseInt(fmt.Sprint(raw), 10, 64); err == nil {
			t = time.UnixMilli(ms).UTC()
		}
	}
	if t.IsZero() {
		t = time.Now().UTC()
	}
	var ask *Ask
	if raw, exists := en.Values["ask"]; exists {
		if b := fmt.Sprint(raw); b != "" {
			var a Ask
			if err := json.Unmarshal([]byte(b), &a); err == nil {
				ask = &a
			}
		}
	}
	return Message{
		ID:      en.ID,
		Role:    Role(fieldString(en.Values, "role")),
		Content: fieldString(en.Values, "content"),
		Agent:   fieldString(en.Values, "agent"),
		Tool:    fieldString(en.Values, "tool"),
		OK:      ok,
		Ask:     ask,
		Time:    t,
	}
}
