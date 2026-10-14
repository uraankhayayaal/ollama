// Тесты набора промпт-инъекций сессии (domain.InjectionSet): адресация по ID
// (Upsert вместо Add, честный Remove) и потокобезопасная выдача копии.
package domain

import "testing"

func TestInjectionSetListEmpty(t *testing.T) {
	var s InjectionSet
	if got := s.List(); got != nil {
		t.Fatalf("пустой набор → nil, got %v", got)
	}
}

func TestInjectionSetAddAndCopy(t *testing.T) {
	var s InjectionSet
	s.Add(Injection{ID: "a", Content: "1"})
	s.Add(Injection{ID: "b", Content: "2"})

	got := s.List()
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("List = %+v", got)
	}
	got[0].ID = "mutated"
	if s.List()[0].ID != "a" {
		t.Fatalf("List должен возвращать копию")
	}
}

func TestInjectionSetUpsertAddsAndReplaces(t *testing.T) {
	var s InjectionSet
	if err := s.Upsert(Injection{ID: "", Content: "x"}); err == nil {
		t.Fatal("пустой id — ошибка")
	}
	if err := s.Upsert(Injection{ID: "a", Content: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Injection{ID: "b", Content: "2"}); err != nil {
		t.Fatal(err)
	}
	// Повтор с тем же id перезаписывает, не плодя дубль и сохраняя позицию.
	if err := s.Upsert(Injection{ID: "a", Content: "1b"}); err != nil {
		t.Fatal(err)
	}
	got := s.List()
	if len(got) != 2 {
		t.Fatalf("дубль не должен появляться: %+v", got)
	}
	if got[0].ID != "a" || got[0].Content != "1b" {
		t.Fatalf("запись не перезаписана: %+v", got[0])
	}
	if got[1].ID != "b" {
		t.Fatalf("порядок сохранён: %+v", got)
	}
}

func TestInjectionSetRemove(t *testing.T) {
	var s InjectionSet
	s.Add(Injection{ID: "a"})
	s.Add(Injection{ID: "b"})
	s.Add(Injection{ID: "c"})

	if !s.Remove("b") {
		t.Fatal("существующая инъекция → true")
	}
	got := s.List()
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("удаление неверно: %+v", got)
	}
	if s.Remove("missing") {
		t.Fatal("отсутствующая инъекция → false")
	}
	if len(s.List()) != 2 {
		t.Fatalf("пропавшее удаление не должно менять набор")
	}
}
