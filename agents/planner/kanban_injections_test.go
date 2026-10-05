package planner

import (
	"context"
	"os"
	"strings"
	"testing"

	"ai/agents"
	"ai/board"
	"ai/projects"
	"ai/runner"
	"ai/tools"

	"github.com/alicebob/miniredis/v2"
)

// injCapturingProvider — фейковый провайдер, который запоминает, что увидел
// специалист. Специалист идёт через настоящий runner: именно runner собирает
// запрос с инъекциями (Generate у kanbanProvider инъекции не применяет).
type injCapturingProvider struct {
	specialistPrompts []string
	// onFirstSpecialist — хук, выполняемый при ПЕРВОМ обращении специалиста к
	// модели: позволяет дописать инъекцию в доску уже после старта цикла.
	onFirstSpecialist func()
	// afterSpecialistPrompt — хук после того, как промпт раунда уже собран;
	// аргумент — номер раунда (с единицы). Позволяет менять инъекции между
	// раундами: правка видна СЛЕДУЮЩЕМУ запросу.
	afterSpecialistPrompt func(round int)
	sawLateInjection      bool
	hooked                bool
	specialistRounds      int
}

func (p *injCapturingProvider) ChatOnce(ctx context.Context, agent agents.Agent, msgs []runner.Message) (*runner.ModelReply, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	if strings.HasPrefix(umsg, "Ты — специалист") {
		var sb strings.Builder
		for _, m := range msgs {
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		}
		p.specialistPrompts = append(p.specialistPrompts, sb.String())
		if !p.hooked {
			p.hooked = true
			if p.onFirstSpecialist != nil {
				p.onFirstSpecialist()
			}
		}
		p.specialistRounds++
		if p.afterSpecialistPrompt != nil {
			p.afterSpecialistPrompt(p.specialistRounds)
		}
		if strings.Contains(sb.String(), "ДОПИСАНО-ПО-ХОДУ") {
			p.sawLateInjection = true
		}
		// Первые два ответа — с вызовом инструмента, чтобы цикл продолжился и
		// изменение инъекций было видно СЛЕДУЮЩЕМУ запросу, а не только
		// первому.
		if p.specialistRounds <= 2 {
			return &runner.ModelReply{
				FinishReason: "tool_calls",
				ToolCalls:    []tools.ToolCall{{Name: "ReadFiles", Arguments: "{}"}},
			}, nil
		}
		return &runner.ModelReply{Content: "Задача выполнена.", FinishReason: "stop"}, nil
	}
	return &runner.ModelReply{}, nil
}

func (p *injCapturingProvider) Generate(ctx context.Context, agent agents.Agent) (*runner.AgentResponse, error) {
	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}
	if strings.HasPrefix(umsg, "Декомпозируй эпик") {
		return &runner.AgentResponse{Content: leadDecompositionJSON}, nil
	}
	return runner.Generate(ctx, p, agent)
}

// newInjKanban создаёт оркестратор поверх in-memory Redis с чистой доской
// проекта и убирает за собой его рабочий каталог.
func newInjKanban(t *testing.T, name string, provider *injCapturingProvider) (*KanbanRunner, *board.Store) {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: name})
	dir := projects.ProjectDir(name)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return NewKanbanRunner(provider, store), store
}

// Инъекции задачи доходят до исполнителя ровно этой задачи. Это и есть смысл
// «инъекций в рамках конкретных задач»: текст из доски попадает в промпт
// специалиста, а не в общий конфиг приложения.
func TestTaskInjectionReachesOwnSpecialist(t *testing.T) {
	ctx := context.Background()
	provider := &injCapturingProvider{}
	kr, store := newInjKanban(t, "inj-task", provider)

	// Заводим эпик и две задачи сразу с нужными статусами, чтобы специалист
	// стартовал с инъекцией (done->ready не разрешён переходом).
	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Приложение"}}); err != nil {
		t.Fatal(err)
	}
	// Роли разные: одна задача на специалиста за цикл, при одинаковой роли
	// вторая задача просто ждала бы и проверка утечки была бы невозможна.
	roles := map[string]string{"T-01": "backend", "T-02": "frontend"}
	for _, id := range []string{"T-01", "T-02"} {
		if err := store.CreateTask(ctx, &board.Task{
			TaskSpec: board.TaskSpec{TaskID: id, Title: "Задача " + id, AssignedRole: roles[id]},
			EpicID:   "ARCH-01",
			Assignee: id + "-специалист",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AddTaskInjection(ctx, "T-01", board.Injection{
		Name: "testify", Target: board.InjectionTargetSystem, Content: "ПИШИ ТЕСТЫ НА TESTIFY",
	}); err != nil {
		t.Fatalf("AddTaskInjection: %v", err)
	}

	// Оркестрация без нового запроса пользователя: доска уже готова.
	if _, err := kr.phaseReady(ctx); err != nil {
		t.Fatalf("phaseReady: %v", err)
	}
	if _, err := kr.phaseExecute(ctx); err != nil {
		t.Fatalf("phaseExecute: %v", err)
	}

	if len(provider.specialistPrompts) == 0 {
		t.Fatal("специалист ни разу не запустился")
	}

	var withInj, withoutInj int
	for _, prompt := range provider.specialistPrompts {
		if strings.Contains(prompt, "ПИШИ ТЕСТЫ НА TESTIFY") {
			withInj++
		} else {
			withoutInj++
		}
	}
	if withInj == 0 {
		t.Fatalf("инъекция T-01 не попала ни в один промпт специалиста (%d шт.)", len(provider.specialistPrompts))
	}
	// Вторая задача инъекцию получить не должна.
	if withoutInj == 0 {
		t.Errorf("инъекция T-01 попала в промпты всех задач — утекла в чужую задачу")
	}
}

// Удаление инъекции посреди прогона действует: агентский цикл перечитывает
// задачу перед каждым запросом, поэтому текст исчезает из промпта, а не
// доживает до конца цикла из стартового снимка.
func TestTaskInjectionRemovedDuringRunStopsApplying(t *testing.T) {
	ctx := context.Background()
	provider := &injCapturingProvider{}
	kr, store := newInjKanban(t, "inj-del", provider)

	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Приложение"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-01", Title: "Задача", AssignedRole: "backend"},
		EpicID:   "ARCH-01",
	}); err != nil {
		t.Fatal(err)
	}
	list, err := store.AddTaskInjection(ctx, "T-01", board.Injection{
		Name: "временная", Target: board.InjectionTargetSystem, Content: "ВРЕМЕННОЕ-УКАЗАНИЕ",
	})
	if err != nil {
		t.Fatalf("AddTaskInjection: %v", err)
	}
	injID := list[0].ID

	// Удаляем инъекцию сразу после того, как первый промпт ушёл модели: он
	// должен был её увидеть, а второй и третий — уже нет.
	round := 0
	provider.afterSpecialistPrompt = func(n int) {
		round = n
		if n == 1 {
			if _, _, err := store.RemoveTaskInjection(ctx, "T-01", injID); err != nil {
				t.Errorf("RemoveTaskInjection: %v", err)
			}
		}
	}

	if _, err := kr.phaseReady(ctx); err != nil {
		t.Fatalf("phaseReady: %v", err)
	}
	if _, err := kr.phaseExecute(ctx); err != nil {
		t.Fatalf("phaseExecute: %v", err)
	}
	if round < 3 {
		t.Fatalf("специалиста было всего %d раунда — проверка удаления не удалась", round)
	}
	const marker = "ВРЕМЕННОЕ-УКАЗАНИЕ"
	if !strings.Contains(provider.specialistPrompts[0], marker) {
		t.Errorf("раунд 1: инъекция должна была быть в промпте: %q", provider.specialistPrompts[0])
	}
	for i, prompt := range provider.specialistPrompts[1:] {
		if strings.Contains(prompt, marker) {
			t.Errorf("раунд %d: инъекция жива после удаления с доски", i+2)
		}
	}
}

// Инъекция, добавленная ПОСЛЕ старта прогона, видна модели: цикл читает задачу
// перед каждым обращением (живой источник), а не один раз на старте.
func TestTaskInjectionAddedDuringRunApplies(t *testing.T) {
	ctx := context.Background()
	provider := &injCapturingProvider{}
	kr, store := newInjKanban(t, "inj-live", provider)

	if err := store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "ARCH-01", Title: "Приложение"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, &board.Task{
		TaskSpec: board.TaskSpec{TaskID: "T-01", Title: "Задача", AssignedRole: "backend"},
		EpicID:   "ARCH-01",
	}); err != nil {
		t.Fatal(err)
	}

	// Хук специалиста: как только цикл начался, дописываем инъекцию в доску.
	provider.onFirstSpecialist = func() {
		if _, err := store.AddTaskInjection(ctx, "T-01", board.Injection{
			ID: "поздняя", Name: "поздняя", Target: board.InjectionTargetSystem,
			Content: "ДОПИСАНО-ПО-ХОДУ",
		}); err != nil {
			t.Errorf("AddTaskInjection в ходе прогона: %v", err)
		}
	}

	if _, err := kr.phaseReady(ctx); err != nil {
		t.Fatalf("phaseReady: %v", err)
	}
	if _, err := kr.phaseExecute(ctx); err != nil {
		t.Fatalf("phaseExecute: %v", err)
	}
	if !provider.sawLateInjection {
		t.Error("инъекция, добавленная в середине цикла, не попала в промпт модели")
	}
}
