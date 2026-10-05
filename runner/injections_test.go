package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai/agents"
	"ai/board"
	"ai/tools"
)

// injSourceAgent — агент с инъекциями уровня assistant (интерфейс
// GetInjections) и именем роли для условий when.
type injSourceAgent struct {
	fakeAgent
	injections []board.Injection
}

func (a *injSourceAgent) GetInjections() []board.Injection { return a.injections }

func (a *injSourceAgent) GetInjectionRole() string { return "backend" }

// testGenerateCtx — Generate с произвольным контекстом (инъекции, resume).
func testGenerateCtx(t *testing.T, ctx context.Context, agent agents.Agent, provider ChatProvider) *AgentResponse {
	t.Helper()
	resp, err := Generate(ctx, provider, agent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return resp
}

// systemOf склеивает системные сообщения запроса (в цикле их обычно одно).
func systemOf(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == "system" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(m.Content)
		}
	}
	return b.String()
}

// allOf склеивает содержимое всех сообщений запроса.
func allOf(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// toolCallReply — ответ с вызовом инструмента (цикл продолжается).
func toolCallReply(name string) *ModelReply {
	return &ModelReply{
		FinishReason: "tool_calls",
		ToolCalls:    []tools.ToolCall{{Name: name, Arguments: "{}"}},
	}
}

func doneReply() *ModelReply { return &ModelReply{Content: "готово", FinishReason: "stop"} }

// Инъекция из живого источника видна модели уже в первом запросе.
func TestTaskInjectionReachesModelOnFirstCall(t *testing.T) {
	agent := &fakeAgent{}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{doneReply()}}}

	ctx := board.WithInjectionSource(context.Background(), func(context.Context) ([]board.Injection, error) {
		return []board.Injection{{
			Name: "живая", Scope: board.InjectionScopeRuntime, Target: board.InjectionTargetSystem,
			Position: board.InjectionPosAppend, Content: "ИНЪЕКЦИЯ-ЗАДАЧИ",
		}}, nil
	})
	resp := testGenerateCtx(t, ctx, agent, provider)
	if resp == nil {
		t.Fatal("resp == nil")
	}
	if len(provider.received) != 1 {
		t.Fatalf("запросов к модели = %d, want 1", len(provider.received))
	}
	if !strings.Contains(systemOf(provider.received[0]), "ИНЪЕКЦИЯ-ЗАДАЧИ") {
		t.Errorf("в системном промпте нет инъекции: %q", systemOf(provider.received[0]))
	}
}

// Главное свойство: правка инъекций видна СЛЕДУЮЩЕМУ обращению к модели, то
// есть уже в середине идущего цикла. Источник переключается после первого
// запроса (как если бы пользователь правил доску, пока агент работает).
func TestTaskInjectionAppliedOnNextModelCall(t *testing.T) {
	agent := &fakeAgent{}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		toolCallReply("ReadFiles"),
		doneReply(),
	}}}

	var calls int
	ctx := board.WithInjectionSource(context.Background(), func(context.Context) ([]board.Injection, error) {
		calls++
		if calls == 1 {
			return []board.Injection{{
				Name: "первая", Scope: board.InjectionScopeRuntime, Target: board.InjectionTargetSystem,
				Position: board.InjectionPosAppend, Content: "ПЕРВАЯ-ИНЪЕКЦИЯ",
			}}, nil
		}
		return []board.Injection{{
			Name: "вторая", Scope: board.InjectionScopeRuntime, Target: board.InjectionTargetSystem,
			Position: board.InjectionPosAppend, Content: "ВТОРАЯ-ИНЪЕКЦИЯ",
		}}, nil
	})

	resp := testGenerateCtx(t, ctx, agent, provider)
	if resp == nil {
		t.Fatal("resp == nil")
	}
	if len(provider.received) != 2 {
		t.Fatalf("запросов к модели = %d, want 2", len(provider.received))
	}
	r1, r2 := systemOf(provider.received[0]), systemOf(provider.received[1])
	if !strings.Contains(r1, "ПЕРВАЯ-ИНЪЕКЦИЯ") {
		t.Errorf("раунд 1 без первой инъекции: %q", r1)
	}
	if !strings.Contains(r2, "ВТОРАЯ-ИНЪЕКЦИЯ") {
		t.Errorf("раунд 2 без второй инъекции: %q", r2)
	}
	if strings.Contains(r2, "ПЕРВАЯ-ИНЪЕКЦИЯ") {
		t.Errorf("раунд 2 всё ещё несёт первую инъекцию: %q", r2)
	}
}

// Инъекция не «впечатывается» в историю: каждый раунд применяется свежий
// набор, поэтому текст не накапливается и не дублируется в промпте.
func TestTaskInjectionNotDuplicatedAcrossRounds(t *testing.T) {
	agent := &fakeAgent{}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		toolCallReply("ReadFiles"),
		doneReply(),
	}}}

	ctx := board.WithInjectionSource(context.Background(), func(context.Context) ([]board.Injection, error) {
		return []board.Injection{{
			Name: "постоянная", Scope: board.InjectionScopeRuntime, Target: board.InjectionTargetSystem,
			Position: board.InjectionPosAppend, Content: "ПОВТОРЯТЬ-НЕЛЬЗЯ",
		}}, nil
	})
	testGenerateCtx(t, ctx, agent, provider)

	if len(provider.received) != 2 {
		t.Fatalf("запросов к модели = %d, want 2", len(provider.received))
	}
	for i, msgs := range provider.received {
		if n := strings.Count(allOf(msgs), "ПОВТОРЯТЬ-НЕЛЬЗЯ"); n != 1 {
			t.Errorf("раунд %d: вхождений инъекции = %d, want 1", i+1, n)
		}
	}
}

// Resume-сегмент тоже получает инъекции: раньше ветка возобновления собирала
// историю из чекпоинта и уходила в модель БЕЗ инъекций.
func TestTaskInjectionAppliedOnResumeSegment(t *testing.T) {
	agent := &fakeAgent{}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{doneReply()}}}

	resume := &ResumeState{
		Rounds: 2,
		Messages: []Message{
			{Role: "system", Content: "ты агент"},
			{Role: "user", Content: "напиши код"},
			{Role: "assistant", Content: "берусь"},
			{Role: "user", Content: "результат инструмента"},
		},
	}
	ctx := WithResumeState(board.WithInjectionSource(context.Background(), func(context.Context) ([]board.Injection, error) {
		return []board.Injection{{
			Name: "resume-инъекция", Scope: board.InjectionScopeRuntime, Target: board.InjectionTargetSystem,
			Position: board.InjectionPosAppend, Content: "ИНЪЕКЦИЯ-НА-RESUME",
		}}, nil
	}), resume)

	resp := testGenerateCtx(t, ctx, agent, provider)
	if resp == nil {
		t.Fatal("resp == nil")
	}
	if len(provider.received) != 1 {
		t.Fatalf("запросов к модели = %d, want 1", len(provider.received))
	}
	got := allOf(provider.received[0])
	if !strings.Contains(got, "ИНЪЕКЦИЯ-НА-RESUME") {
		t.Errorf("resume-запрос без инъекции: %q", got)
	}
	if !strings.Contains(got, "результат инструмента") {
		t.Errorf("resume-запрос потерял историю чекпоинта: %q", got)
	}
}

// Снапшот (NewInjectionContext) и живой источник работают вместе: правка на
// доске перекрывает стартовый снимок (у них один id — last-wins).
func TestTaskInjectionLiveSourceOverridesSnapshot(t *testing.T) {
	agent := &fakeAgent{}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{doneReply()}}}

	ctx := board.NewInjectionContext(context.Background(), []board.Injection{{
		ID: "inj-1", Name: "из снимка", Scope: board.InjectionScopeRuntime,
		Target: board.InjectionTargetSystem, Position: board.InjectionPosAppend, Content: "СТАРЫЙ-ТЕКСТ",
	}})
	ctx = board.WithInjectionSource(ctx, func(context.Context) ([]board.Injection, error) {
		return []board.Injection{{
			ID: "inj-1", Name: "из доски", Scope: board.InjectionScopeRuntime,
			Target: board.InjectionTargetSystem, Position: board.InjectionPosAppend, Content: "НОВЫЙ-ТЕКСТ",
		}}, nil
	})

	testGenerateCtx(t, ctx, agent, provider)
	got := systemOf(provider.received[0])
	if !strings.Contains(got, "НОВЫЙ-ТЕКСТ") {
		t.Errorf("в промпте нет живой версии инъекции: %q", got)
	}
	if strings.Contains(got, "СТАРЫЙ-ТЕКСТ") {
		t.Errorf("в промпте остался снимок инъекции: %q", got)
	}
	if n := strings.Count(got, "НОВЫЙ-ТЕКСТ"); n != 1 {
		t.Errorf("вхождений живой версии = %d, want 1", n)
	}
}

// Удаление инъекций действует немедленно: живой источник вернул пустой список
// (доска прочитана, инъекций у задачи больше нет) — значит применяться не
// должна ничего, даже при непустом стартовом снапшоте. Раньше пустой список
// трактовался как «не удалось прочитать» и откатывался к снапшоту, из-за чего
// удалённая инъекция жила до конца прогона.
func TestTaskInjectionDeletionBeatsSnapshot(t *testing.T) {
	agent := &fakeAgent{}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		toolCallReply("ReadFiles"),
		doneReply(),
	}}}

	// Снапшот на старте прогона: инъекция была в задаче.
	snapshot := []board.Injection{{
		ID: "inj-1", Name: "удаляемая", Scope: board.InjectionScopeRuntime,
		Target: board.InjectionTargetSystem, Position: board.InjectionPosAppend,
		Content: "УДАЛЁННАЯ-НО-ЖИВЁТ",
	}}
	calls := 0
	ctx := board.NewInjectionContext(context.Background(), snapshot)
	ctx = board.WithInjectionSource(ctx, func(context.Context) ([]board.Injection, error) {
		calls++
		if calls == 1 {
			return snapshot, nil // первый запрос: инъекция ещё на доске
		}
		return nil, nil // пользователь удалил её между раундами
	})

	testGenerateCtx(t, ctx, agent, provider)
	if len(provider.received) != 2 {
		t.Fatalf("запросов к модели = %d, want 2", len(provider.received))
	}
	if !strings.Contains(allOf(provider.received[0]), "УДАЛЁННАЯ-НО-ЖИВЁТ") {
		t.Fatalf("раунд 1 должен был содержать инъекцию: %q", allOf(provider.received[0]))
	}
	if got := allOf(provider.received[1]); strings.Contains(got, "УДАЛЁННАЯ-НО-ЖИВЁТ") {
		t.Errorf("после удаления инъекция всё ещё в промпте: %q", got)
	}
}

// Удаление одной из нескольких инъекций действует: оставшаяся продолжает
// применяться, удалённая исчезает со следующего запроса.
func TestTaskInjectionPartialDeletionKeepsTheRest(t *testing.T) {
	agent := &fakeAgent{}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		toolCallReply("ReadFiles"),
		doneReply(),
	}}}

	snapshot := []board.Injection{
		{ID: "keep", Name: "остаётся", Scope: board.InjectionScopeRuntime,
			Target: board.InjectionTargetSystem, Position: board.InjectionPosAppend, Content: "ОСТАЁТСЯ"},
		{ID: "drop", Name: "удаляется", Scope: board.InjectionScopeRuntime,
			Target: board.InjectionTargetSystem, Position: board.InjectionPosAppend, Content: "УДАЛЯЕТСЯ"},
	}
	calls := 0
	ctx := board.NewInjectionContext(context.Background(), snapshot)
	ctx = board.WithInjectionSource(ctx, func(context.Context) ([]board.Injection, error) {
		calls++
		if calls == 1 {
			return snapshot, nil
		}
		return snapshot[0:1], nil // пользователь снёс только вторую
	})

	testGenerateCtx(t, ctx, agent, provider)
	if len(provider.received) != 2 {
		t.Fatalf("запросов к модели = %d, want 2", len(provider.received))
	}
	if first := allOf(provider.received[0]); !strings.Contains(first, "ОСТАЁТСЯ") || !strings.Contains(first, "УДАЛЯЕТСЯ") {
		t.Fatalf("раунд 1 должен был содержать обе инъекции: %q", first)
	}
	second := allOf(provider.received[1])
	if !strings.Contains(second, "ОСТАЁТСЯ") {
		t.Errorf("оставшаяся инъекция пропала: %q", second)
	}
	if strings.Contains(second, "УДАЛЯЕТСЯ") {
		t.Errorf("удалённая инъекция всё ещё применяется: %q", second)
	}
}

// Ошибка живого источника (доска недоступна) — единственный повод откатиться на
// снапшот: лучше применить прежний текст, чем молча выключить инструкцию.
func TestTaskInjectionSourceErrorFallsBackToSnapshot(t *testing.T) {
	agent := &fakeAgent{}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{
		toolCallReply("ReadFiles"),
		doneReply(),
	}}}

	snapshot := []board.Injection{{
		ID: "inj-1", Name: "из снимка", Scope: board.InjectionScopeRuntime,
		Target: board.InjectionTargetSystem, Position: board.InjectionPosAppend,
		Content: "ОТКАТ-ПРИ-ОШИБКЕ",
	}}
	ctx := board.NewInjectionContext(context.Background(), snapshot)
	ctx = board.WithInjectionSource(ctx, func(context.Context) ([]board.Injection, error) {
		return nil, errors.New("доска недоступна")
	})

	testGenerateCtx(t, ctx, agent, provider)
	for i, msgs := range provider.received {
		if !strings.Contains(allOf(msgs), "ОТКАТ-ПРИ-ОШИБКЕ") {
			t.Errorf("раунд %d: при ошибке источника инъекция из снимка потерялась", i+1)
		}
	}
}

// Условия when получают реальные значения прогона: раньше model/provider/
// project/role/tools/turn были пустыми, и `when: "project == \"mytrip\""`
// не могло сработать никогда.
func TestInjectionWhenContextFilledWithRealValues(t *testing.T) {
	cases := []struct {
		name    string
		when    string
		content string
		want    bool
	}{
		{"совпадение проекта", `project == "mytrip"`, "СОВПАЛ-ПРОЕКТ", true},
		{"чужой проект", `project == "другой"`, "НЕ-СОВПАЛ", false},
		{"роль из scope", `role == "backend"`, "СОВПАЛА-РОЛЬ", true},
		{"роль агента", `role == "backend"`, "РОЛЬ-ИЗ-АГЕНТА", true},
		{"модель", `model == "qwen3:30b"`, "СОВПАЛА-МОДЕЛЬ", true},
		{"провайдер", `provider == "ollama"`, "СОВПАЛ-ПРОВАЙДЕР", true},
		{"задача", `task_id == "FEL-02"`, "СОВПАЛА-ЗАДАЧА", true},
		{"инструменты", `tools != ""`, "ЕСТЬ-ИНСТРУМЕНТЫ", true},
		{"файлы", `has_files == "true"`, "ЕСТЬ-ФАЙЛЫ", true},
		{"отрицание", `!(role == "frontend")`, "НЕ-ФРОНТЕНД", true},
		{"составное условие", `role == "backend" && project == "mytrip"`, "ОБА-УСЛОВИЯ", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := &injSourceAgent{} // GetInjectionRole() = "backend"

			ctx := board.WithInjectionScope(context.Background(), board.InjectionScope{
				Project: "mytrip",
				TaskID:  "FEL-02",
				Role:    "backend",
			})
			ctx = board.NewInjectionContext(ctx, []board.Injection{{
				Name: tc.name, Scope: board.InjectionScopeRuntime,
				Target: board.InjectionTargetSystem, Position: board.InjectionPosAppend,
				Content: tc.content, When: tc.when,
			}})

			// Провайдер, называющий себя: переменные model/provider в when
			// заполняются из интерфейсов ModelName/ProviderName.
			prov := &namedRecordingProvider{
				model: "qwen3:30b", provider: "ollama",
				fakeChatProvider: fakeChatProvider{replies: []*ModelReply{doneReply()}},
			}

			testGenerateCtx(t, ctx, agent, prov)
			if len(prov.received) != 1 {
				t.Fatalf("запросов = %d, want 1", len(prov.received))
			}
			got := allOf(prov.received[0])
			has := strings.Contains(got, tc.content)
			if has != tc.want {
				t.Errorf("when=%q применилась=%v, want %v (промпт: %q)", tc.when, has, tc.want, got)
			}
		})
	}
}

// namedRecordingProvider — recordingProvider, который ещё и называет модель и
// провайдера (интерфейсы ModelName/ProviderName).
type namedRecordingProvider struct {
	fakeChatProvider
	received [][]Message
	model    string
	provider string
}

func (p *namedRecordingProvider) ChatOnce(ctx context.Context, agent agents.Agent, messages []Message) (*ModelReply, error) {
	cp := make([]Message, len(messages))
	copy(cp, messages)
	p.received = append(p.received, cp)
	return p.fakeChatProvider.ChatOnce(ctx, agent, messages)
}

func (p *namedRecordingProvider) ModelName() string    { return p.model }
func (p *namedRecordingProvider) ProviderName() string { return p.provider }

// Инъекции уровня assistant (Agent.GetInjections) доходят до модели.
func TestAssistantInjectionsReachModel(t *testing.T) {
	agent := &injSourceAgent{injections: []board.Injection{{
		Name: "ассистента", Scope: board.InjectionScopeAssistant, Target: board.InjectionTargetSystem,
		Position: board.InjectionPosPrepend, Content: "ОТ-АССИСТЕНТА",
	}}}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{doneReply()}}}

	testGenerateAny(t, agent, provider)
	if !strings.Contains(systemOf(provider.received[0]), "ОТ-АССИСТЕНТА") {
		t.Errorf("в промпте нет инъекции ассистента: %q", systemOf(provider.received[0]))
	}
}

// Порядок scope сохраняется в промпте: assistant раньше task, поэтому его текст
// идёт выше по системному сообщению.
func TestInjectionScopeOrderInPrompt(t *testing.T) {
	agent := &injSourceAgent{injections: []board.Injection{{
		Name: "asst", Scope: board.InjectionScopeAssistant, Target: board.InjectionTargetSystem,
		Position: board.InjectionPosAppend, Content: "БЛОК-ASST",
	}}}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{doneReply()}}}

	ctx := board.NewInjectionContext(context.Background(), []board.Injection{{
		Name: "task", Scope: board.InjectionScopeRuntime, Target: board.InjectionTargetSystem,
		Position: board.InjectionPosAppend, Content: "БЛОК-TASK",
	}})
	testGenerateCtx(t, ctx, agent, provider)

	got := systemOf(provider.received[0])
	ia := strings.Index(got, "БЛОК-ASST")
	it := strings.Index(got, "БЛОК-TASK")
	if ia < 0 || it < 0 {
		t.Fatalf("в промпте нет одного из блоков: %q", got)
	}
	if ia > it {
		t.Errorf("assistant-блок должен идти раньше task-блока: %q", got)
	}
}

// Выключенная инъекция (enabled=false) не доходит до модели.
func TestInjectionEnabledFalseNotApplied(t *testing.T) {
	agent := &fakeAgent{}
	provider := &recordingProvider{fakeChatProvider: fakeChatProvider{replies: []*ModelReply{doneReply()}}}

	off := false
	ctx := board.NewInjectionContext(context.Background(), []board.Injection{{
		Name: "выключенная", Scope: board.InjectionScopeRuntime, Target: board.InjectionTargetSystem,
		Position: board.InjectionPosAppend, Content: "ВЫКЛЮЧЕННЫЙ-ТЕКСТ", Enabled: &off,
	}})
	testGenerateCtx(t, ctx, agent, provider)

	if strings.Contains(allOf(provider.received[0]), "ВЫКЛЮЧЕННЫЙ-ТЕКСТ") {
		t.Errorf("выключенная инъекция попала в промпт: %q", allOf(provider.received[0]))
	}
}
