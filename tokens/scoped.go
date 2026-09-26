// Счётчики токенов по единицам работы — задачам, эпикам и служебным фазам
// оркестрации (Ф-2 PLAN-2026-09-19-done-epic-task-token.md).
//
// Оркестратор помечает каждый вызов Generate scope'ом (runevents.WithScope),
// сервер по событию копит расход в Redis-хэш tokens:<project>:scoped:<scope>
// (поля "in"/"out"). Когда единица работы доходит до терминального статуса,
// оркестратор снимает накопленное (GetScoped), записывает итог в сущность
// доски (board.FinalizeTaskTokens/FinalizeEpicTokens) и обнуляет счётчик
// (ResetScoped) — так «сырой» расход не задваивается в сводке проекта.
package tokens

import (
	"context"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Имена scope'ов единиц работы. Формат: "task:<id>" / "epic:<id>" либо простое
// имя служебной фазы. Пустой scope — расход без атрибуции (счётчик проекта).
const (
	// ScopeArchitecture — генерация бэклога эпиков и ревизия архитектора.
	ScopeArchitecture = "architecture"
	// ScopeBugs — работа с багрепортами (QA и экспертиза архитектора).
	ScopeBugs = "bugs"
)

// Префиксы scope'ов сущностей доски.
const (
	scopeTaskPrefix = "task:"
	scopeEpicPrefix = "epic:"
)

// ScopeTask возвращает scope расхода токенов на задачу.
func ScopeTask(id string) string { return scopeTaskPrefix + id }

// ScopeEpic возвращает scope расхода токенов на эпик.
func ScopeEpic(id string) string { return scopeEpicPrefix + id }

// ParseScope разбирает scope на вид и идентификатор единицы. Для
// "task:<id>"/"epic:<id>" возвращает ("task"/"epic", id, true); для
// служебных scope'ов ("architecture", "bugs") — ("architecture"/"bugs", "",
// true); для пустого/неизвестного — ("", "", false). Разбор устойчив к
// мусору в Redis: неизвестное значение просто игнорируется вызывающим кодом.
func ParseScope(scope string) (kind, id string, ok bool) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "", "", false
	}
	if rest, found := strings.CutPrefix(scope, scopeTaskPrefix); found && rest != "" {
		return "task", rest, true
	}
	if rest, found := strings.CutPrefix(scope, scopeEpicPrefix); found && rest != "" {
		return "epic", rest, true
	}
	switch scope {
	case ScopeArchitecture, ScopeBugs:
		return scope, "", true
	}
	return "", "", false
}

// ScopedKey возвращает Redis-ключ счётчика scope'а.
func (s *Store) ScopedKey(scope string) string {
	return s.key + ":scoped:" + scope
}

// AddScoped атомарно прибавляет токены к счётчику scope'а. Пустой scope —
// расход уходит только в общий счётчик проекта (Add), без атрибуции.
func (s *Store) AddScoped(ctx context.Context, scope string, in, out int64) error {
	if _, _, ok := ParseScope(scope); !ok {
		return nil
	}
	key := s.ScopedKey(scope)
	if _, err := s.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		p.HIncrBy(ctx, key, "in", in)
		p.HIncrBy(ctx, key, "out", out)
		return nil
	}); err != nil {
		return fmt.Errorf("tokens: инкремент %s: %w", key, err)
	}
	return nil
}

// GetScoped возвращает накопленные по scope суммы (вход/выход). Неизвестный
// scope — нули без ошибки.
func (s *Store) GetScoped(ctx context.Context, scope string) (in, out int64, err error) {
	if _, _, ok := ParseScope(scope); !ok {
		return 0, 0, nil
	}
	vals, err := s.client.HMGet(ctx, s.ScopedKey(scope), "in", "out").Result()
	if err != nil {
		return 0, 0, fmt.Errorf("tokens: чтение %s: %w", s.ScopedKey(scope), err)
	}
	return hashInt(vals[0]), hashInt(vals[1]), nil
}

// ResetScoped обнуляет счётчик scope'а (после фиксации итога в доске).
func (s *Store) ResetScoped(ctx context.Context, scope string) error {
	if _, _, ok := ParseScope(scope); !ok {
		return nil
	}
	if err := s.client.Del(ctx, s.ScopedKey(scope)).Err(); err != nil {
		return fmt.Errorf("tokens: сброс %s: %w", s.ScopedKey(scope), err)
	}
	return nil
}
