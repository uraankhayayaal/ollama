package domain

import (
	"fmt"
	"sync"
)

// InjectionSet — потокобезопасный набор промпт-инъекций уровня сессии с
// адресацией по ID. Инъекции применяются «на лету»: со следующего запроса к
// модели в этой сессии. Хранится в памяти (персистентность пока не требуется).
//
// Вынесено из HTTP-слоя (`server/session.go`, Этап 6): адресация по ID
// (Upsert вместо Add, честный Remove) — правило домена, а не сервера.
type InjectionSet struct {
	mu   sync.Mutex
	injs []Injection
}

// List возвращает копию набора (для потокобезопасной выдачи наружу).
func (s *InjectionSet) List() []Injection {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.injs) == 0 {
		return nil
	}
	out := make([]Injection, len(s.injs))
	copy(out, s.injs)
	return out
}

// Add добавляет инъекцию в конец набора.
func (s *InjectionSet) Add(inj Injection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.injs = append(s.injs, inj)
}

// Upsert добавляет инъекцию, перезаписывая запись с тем же ID. Именно Upsert, а
// не Add: повторная запись с тем же ID должна править инъекцию, а не плодить
// дубли (иначе ID не адресует ровно одну запись и удаление сносит только одну
// из копий). Пустой ID — ошибка.
func (s *InjectionSet) Upsert(inj Injection) error {
	if inj.ID == "" {
		return fmt.Errorf("инъекция: id обязателен")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Injection, 0, len(s.injs)+1)
	replaced := false
	for _, existing := range s.injs {
		if existing.ID == inj.ID {
			out = append(out, inj)
			replaced = true
			continue
		}
		out = append(out, existing)
	}
	if !replaced {
		out = append(out, inj)
	}
	s.injs = out
	return nil
}

// Remove удаляет инъекцию по ID и сообщает, была ли она. Честный возврат
// обязателен: удаление по несуществующему (или несохранившемуся) ID не должно
// отвечать успехом, иначе инъекция продолжает жить.
func (s *InjectionSet) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Injection, 0, len(s.injs))
	found := false
	for _, inj := range s.injs {
		if inj.ID == id {
			found = true
			continue
		}
		out = append(out, inj)
	}
	if found {
		s.injs = out
	}
	return found
}
