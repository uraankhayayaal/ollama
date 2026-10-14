package board

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestInjectionValidateRejectsBadValues(t *testing.T) {
	base := func() Injection {
		return Injection{Name: "ok", Target: InjectionTargetSystem, Position: InjectionPosAppend, Content: "X"}
	}

	cases := []struct {
		name string
		mut  func(*Injection)
		want string
	}{
		{"без имени", func(i *Injection) { i.Name = "  " }, "имя"},
		{"без content", func(i *Injection) { i.Content = "" }, "content"},
		{"content больше лимита", func(i *Injection) {
			i.Content = strings.Repeat("x", MaxInjectionContentSize+1)
		}, "больше лимита"},
		{"неизвестный target", func(i *Injection) { i.Target = "before_tools" }, "target"},
		{"target after_tools", func(i *Injection) { i.Target = "after_tools" }, "target"},
		{"неизвестный scope", func(i *Injection) { i.Scope = "мистика" }, "scope"},
		{"неизвестная position", func(i *Injection) { i.Position = "куда-то" }, "position"},
		{"inject_at_index с отрицательным индексом", func(i *Injection) {
			i.Position = InjectionPosInjectAtIndex
			i.Index = -1
		}, "index"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inj := base()
			tc.mut(&inj)
			err := inj.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want ошибка про %s", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ошибка %q не упоминает %q", err, tc.want)
			}
		})
	}
}

// Корректные записи проходят валидацию во всех комбинациях scope/position.
func TestInjectionValidateAcceptsSupportedCombos(t *testing.T) {
	for _, target := range []string{InjectionTargetSystem, InjectionTargetMessages, InjectionTargetUserLast, InjectionTargetAssistantLast} {
		for _, pos := range []string{InjectionPosPrepend, InjectionPosAppend, InjectionPosReplace, InjectionPosInjectAtIndex} {
			inj := Injection{Name: "n", Target: target, Position: pos, Content: "C", Index: 1}
			if err := inj.Validate(); err != nil {
				t.Errorf("target=%s position=%s: %v", target, pos, err)
			}
		}
	}
}

// scope «task» — синоним runtime: нормализуется и получает тот же приоритет.
func TestInjectionTaskScopeNormalizesToRuntime(t *testing.T) {
	inj := Injection{Name: "n", Scope: InjectionScopeTask, Target: InjectionTargetSystem, Content: "C"}
	inj.Normalize()
	if inj.Scope != InjectionScopeRuntime {
		t.Errorf("scope = %q, want %q", inj.Scope, InjectionScopeRuntime)
	}
	if inj.Position != DefaultInjectionPosition {
		t.Errorf("position = %q, want дефолт %q", inj.Position, DefaultInjectionPosition)
	}
}

// Normalize выдаёт id: инъекция без id не адресовалась бы в DELETE.
func TestInjectionNormalizeAssignsID(t *testing.T) {
	inj := Injection{Name: "n", Target: InjectionTargetSystem, Content: "C"}
	inj.Normalize()
	if inj.ID == "" {
		t.Fatal("Normalize не выдал id")
	}
	if len(inj.ID) != 32 {
		t.Errorf("id = %q, want 32 hex-символа", inj.ID)
	}
	// Явный id не перетирается.
	inj2 := Injection{ID: "мой", Name: "n", Target: InjectionTargetSystem, Content: "C"}
	inj2.Normalize()
	if inj2.ID != "мой" {
		t.Errorf("id = %q, want «мой»", inj2.ID)
	}
}

// Три состояния enabled: nil (активна), true (активна), false (выключена).
func TestInjectionIsEnabledTriState(t *testing.T) {
	inj := Injection{Name: "n"}
	if !inj.IsEnabled() {
		t.Error("без поля enabled инъекция должна быть активна")
	}
	inj.SetEnabled(true)
	if !inj.IsEnabled() {
		t.Error("enabled=true должна быть активна")
	}
	inj.SetEnabled(false)
	if inj.IsEnabled() {
		t.Error("enabled=false должна быть выключена")
	}
}

func TestValidateInjectionsRejectsDuplicateIDs(t *testing.T) {
	inj := []Injection{
		{ID: "dup", Name: "a", Target: InjectionTargetSystem, Content: "A"},
		{ID: "dup", Name: "b", Target: InjectionTargetSystem, Content: "B"},
	}
	err := ValidateInjections(inj)
	if err == nil || !strings.Contains(err.Error(), "дубль id") {
		t.Fatalf("ValidateInjections = %v, want ошибка про дубль id", err)
	}

	// Без id дубли допустимы: адресовать их нельзя, но и конфликта нет.
	noID := []Injection{
		{Name: "a", Target: InjectionTargetSystem, Content: "A"},
		{Name: "a", Target: InjectionTargetSystem, Content: "B"},
	}
	if err := ValidateInjections(noID); err != nil {
		t.Errorf("записи без id: неожиданная ошибка %v", err)
	}
}

// Строгий разбор JSON: опечатка в поле — ошибка, а не молчаливая потеря.
func TestDecodeInjectionsStrict(t *testing.T) {
	var raw []any
	if err := json.Unmarshal([]byte(`[{"name":"n","target":"system","content":"C","psoition":"append"}]`), &raw); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeInjections(raw); err == nil {
		t.Fatal("DecodeInjections принял неизвестное поле")
	}

	if err := json.Unmarshal([]byte(`[{"name":"n","target":"system","content":"C","enabled":false}]`), &raw); err != nil {
		t.Fatal(err)
	}
	list, err := DecodeInjections(raw)
	if err != nil {
		t.Fatalf("DecodeInjections: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	if list[0].IsEnabled() {
		t.Error("enabled=false не распознан")
	}
}

// Принимаются и массив, и одиночный объект (удобно для точечных правок).
func TestDecodeInjectionsAcceptsSingleObject(t *testing.T) {
	var raw any
	if err := json.Unmarshal([]byte(`{"name":"n","target":"system","content":"C"}`), &raw); err != nil {
		t.Fatal(err)
	}
	list, err := DecodeInjections(raw)
	if err != nil {
		t.Fatalf("DecodeInjections: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	if got, err := DecodeInjections(nil); err != nil || got != nil {
		t.Errorf("DecodeInjections(nil) = %+v, %v; want nil, nil", got, err)
	}
}

// Живой источник авторитетен: он читает ту же запись, что и снимок, поэтому
// заменяет его целиком, а не дополняет.
func TestAllInjectionsLiveSourceReplacesSnapshot(t *testing.T) {
	base := context.Background()
	ctx := NewInjectionContext(base, nil) // fast-path: nil-инъекции не трогают контекст
	if InjectionsFromContext(ctx) != nil {
		t.Error("nil-инъекции не должны класться в контекст")
	}

	snapshot := []Injection{{ID: "a", Name: "снимок", Content: "OLD"}}
	live := []Injection{{ID: "a", Name: "доска", Content: "NEW"}, {ID: "b", Name: "новая", Content: "NEW2"}}

	ctx = NewInjectionContext(ctx, snapshot)
	ctx = WithInjectionSource(ctx, func(context.Context) ([]Injection, error) { return live, nil })

	all := AllInjections(ctx)
	if len(all) != 2 {
		t.Fatalf("AllInjections = %d записей, want 2 (только живой источник)", len(all))
	}
	if all[0].Name != "доска" || all[0].Content != "NEW" {
		t.Errorf("правка с доски не заменила снимок: %+v", all[0])
	}

	// Только живой источник без снимка.
	onlyLive := AllInjections(WithInjectionSource(context.Background(), func(context.Context) ([]Injection, error) {
		return live, nil
	}))
	if len(onlyLive) != 2 {
		t.Errorf("AllInjections без снимка = %d, want 2", len(onlyLive))
	}
}

// Удаление ЧАСТИ инъекций посреди прогона тоже должно действовать. Слияние
// «снимок + живой» возвращало бы удалённую запись из снимка — ровно тот
// дефект, что и при удалении всех.
func TestAllInjectionsPartialDeletionWins(t *testing.T) {
	ctx := NewInjectionContext(context.Background(), []Injection{
		{ID: "a", Name: "остаётся", Content: "A"},
		{ID: "b", Name: "удалена", Content: "B"},
	})
	ctx = WithInjectionSource(ctx, func(context.Context) ([]Injection, error) {
		return []Injection{{ID: "a", Name: "остаётся", Content: "A"}}, nil
	})
	got := AllInjections(ctx)
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("AllInjections = %+v, want только оставшаяся запись", got)
	}
}

// Ошибка живого источника (доска недоступна) не ломает цикл: возвращается
// снапшот — лучше применить прежний текст, чем молча выключить инструкцию.
func TestAllInjectionsFallsBackToSnapshotOnSourceError(t *testing.T) {
	ctx := NewInjectionContext(context.Background(), []Injection{{ID: "a", Name: "снимок"}})
	ctx = WithInjectionSource(ctx, func(context.Context) ([]Injection, error) {
		return nil, errors.New("доска недоступна")
	})
	if got := AllInjections(ctx); len(got) != 1 || got[0].Name != "снимок" {
		t.Errorf("AllInjections = %+v, want снапшот", got)
	}
}

// Пустой список БЕЗ ошибки — честный ответ «инъекций больше нет»: снимок
// возвращаться не должен, иначе удалённые с доски инъекции воскресали бы и
// жили до конца прогона.
func TestAllInjectionsEmptyLiveDropsSnapshot(t *testing.T) {
	ctx := NewInjectionContext(context.Background(), []Injection{{ID: "a", Name: "снимок"}})
	ctx = WithInjectionSource(ctx, func(context.Context) ([]Injection, error) {
		return nil, nil
	})
	if got := AllInjections(ctx); len(got) != 0 {
		t.Errorf("AllInjections = %+v, want пусто (удаление должно действовать)", got)
	}
}

func TestInjectionScopeRoundTrip(t *testing.T) {
	if got := InjectionScopeFromContext(context.Background()); got.Project != "" {
		t.Errorf("пустой контекст вернул %+v", got)
	}
	ctx := WithInjectionScope(context.Background(), InjectionScope{Project: "mytrip", TaskID: "FEL-02", Role: "backend"})
	got := InjectionScopeFromContext(ctx)
	if got.Project != "mytrip" || got.TaskID != "FEL-02" || got.Role != "backend" {
		t.Errorf("рамка прогона = %+v", got)
	}
}

// Инъекции задачи: добавление, перезапись по id, удаление — с проверкой, что
// запись действительно лежит в доске (а не «200, как у сессии раньше»).
func TestStoreTaskInjectionsCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01", Title: "Эпик"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: "FEL-01", Title: "Задача"}, EpicID: "ARC-01"}); err != nil {
		t.Fatal(err)
	}

	list, err := s.AddTaskInjection(ctx, "FEL-01", Injection{
		Name: "testify", Target: InjectionTargetSystem, Content: "ПИШИ НА TESTIFY",
	})
	if err != nil {
		t.Fatalf("AddTaskInjection: %v", err)
	}
	if len(list) != 1 || list[0].ID == "" {
		t.Fatalf("список = %+v, want одна запись с id", list)
	}
	injID := list[0].ID

	task, err := s.GetTask(ctx, "FEL-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Injections) != 1 || task.Injections[0].Content != "ПИШИ НА TESTIFY" {
		t.Fatalf("в задаче = %+v", task.Injections)
	}

	// Повторное добавление с тем же id перезаписывает, а не дублирует.
	list, err = s.AddTaskInjection(ctx, "FEL-01", Injection{
		ID: injID, Name: "testify", Target: InjectionTargetSystem, Content: "ОБНОВЛЁННЫЙ ТЕКСТ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Content != "ОБНОВЛЁННЫЙ ТЕКСТ" {
		t.Fatalf("после перезаписи список = %+v", list)
	}

	// Невалидная запись не пишется.
	if _, err := s.AddTaskInjection(ctx, "FEL-01", Injection{Name: "плохая", Target: "system"}); err == nil {
		t.Error("AddTaskInjection принял запись без content")
	}

	// Удаление.
	rest, found, err := s.RemoveTaskInjection(ctx, "FEL-01", injID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(rest) != 0 {
		t.Fatalf("RemoveTaskInjection: found=%v, rest=%+v", found, rest)
	}
	task, _ = s.GetTask(ctx, "FEL-01")
	if len(task.Injections) != 0 {
		t.Fatalf("после удаления в задаче %d инъекций", len(task.Injections))
	}

	// Удаление несуществующего id — found=false, список не меняется.
	if _, err := s.AddTaskInjection(ctx, "FEL-01", Injection{Name: "ещё одна", Target: InjectionTargetSystem, Content: "X"}); err != nil {
		t.Fatal(err)
	}
	_, found, err = s.RemoveTaskInjection(ctx, "FEL-01", "нет-такого")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("RemoveTaskInjection сообщил found для несуществующего id")
	}
}

// Полная замена списка — единственная точка записи, валидирует запись.
func TestStoreSetTaskInjectionsValidates(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateEpic(ctx, &Epic{TaskSpec: TaskSpec{TaskID: "ARC-01", Title: "Эпик"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(ctx, &Task{TaskSpec: TaskSpec{TaskID: "FEL-01", Title: "Задача"}, EpicID: "ARC-01"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskInjections(ctx, "FEL-01", []Injection{{Name: "ok", Target: InjectionTargetSystem, Content: "C"}}); err != nil {
		t.Fatalf("SetTaskInjections: %v", err)
	}
	err := s.SetTaskInjections(ctx, "FEL-01", []Injection{{Name: "плохая", Target: "nonsense", Content: "C"}})
	if err == nil {
		t.Fatal("SetTaskInjections принял неизвестный target")
	}
	task, _ := s.GetTask(ctx, "FEL-01")
	if len(task.Injections) != 1 {
		t.Fatalf("после отказа список изменился: %+v", task.Injections)
	}
	// Пустой список удаляет всё.
	if err := s.SetTaskInjections(ctx, "FEL-01", nil); err != nil {
		t.Fatalf("SetTaskInjections(nil): %v", err)
	}
	task, _ = s.GetTask(ctx, "FEL-01")
	if len(task.Injections) != 0 {
		t.Fatalf("после очистки %d инъекций", len(task.Injections))
	}
}
