package architect

import (
	"ai/board"
	"ai/rag"
	"ai/tools"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// newTestArchitect создаёт архитектора с хранилищем доски поверх in-memory
// Redis (miniredis). Проект размещается во временной директории, чтобы не
// засорять модульный temp/.
func newTestArchitect(t *testing.T) *Architect {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "testproj"})
	a := NewArchitectWithStore("testproj", "Задача пользователя", store)
	a.OutputDir = filepath.Join(t.TempDir(), "testproj")
	return a
}

func TestRequiredToolFirstRound(t *testing.T) {
	a := newTestArchitect(t)
	name, ok := a.RequiredToolFirstRound()
	if !ok || name != SubmitBacklogToolName {
		t.Fatalf("RequiredToolFirstRound = %q, %v", name, ok)
	}
}

func TestToolsIncludeSubmitBacklog(t *testing.T) {
	a := newTestArchitect(t)
	got := a.GetTools()
	names := map[string]bool{}
	for _, td := range got {
		names[td.Name] = true
	}
	for _, want := range []string{"List", "ReadFiles", SubmitBacklogToolName} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
	for _, disallowed := range []string{"WriteFiles", "AppendFile", "DeleteFiles", "Run"} {
		if names[disallowed] {
			t.Errorf("архитектор не должен включать инструмент %q", disallowed)
		}
	}
	// Определение submit_architecture_backlog объявляет задачи в схеме.
	if td := defByName(got, SubmitBacklogToolName); td == nil {
		t.Fatal("не найдено определение submit_architecture_backlog")
	} else if _, ok := td.Parameters["properties"].(map[string]any)["tasks"]; !ok {
		t.Error("схема submit_architecture_backlog не содержит поле tasks")
	}
}

// TestToolsIncludeStudyAndRAG — Ф-1: архитектор работает с RAG/доской: набор
// инструментов содержит семантический поиск (CodeSearch), статус индекса
// (RagIndexStatus) и чтение деталей эпика/задачи (BoardGetEpic/BoardGetTask) —
// контрактов перед правкой.
func TestToolsIncludeStudyAndRAG(t *testing.T) {
	a := newTestArchitect(t)
	names := map[string]bool{}
	for _, td := range a.GetTools() {
		names[td.Name] = true
	}
	for _, want := range []string{
		tools.CodeSearch, tools.RagIndexStatus,
		tools.BoardGetEpic, tools.BoardGetTask,
	} {
		if !names[want] {
			t.Errorf("агент не включает инструмент %q", want)
		}
	}
}

func defByName(defs []tools.ToolDefinition, name string) *tools.ToolDefinition {
	for i := range defs {
		if defs[i].Name == name {
			return &defs[i]
		}
	}
	return nil
}

func TestSubmitBacklogPersistsEpics(t *testing.T) {
	a := newTestArchitect(t)

	args := map[string]any{
		"architecture_summary": "Общее архитектурное решение",
		"tasks": []map[string]any{
			{
				"task_id":          "ARCH-01",
				"title":            "Backend модуль",
				"description":      "Описание",
				"assigned_role":    "Backend Lead",
				"sequence_order":   1,
				"can_run_parallel": true,
				"dependencies":     []string{},
			},
			{
				"task_id":          "ARCH-02",
				"title":            "Инфраструктура",
				"description":      "Описание",
				"assigned_role":    "DevOps Lead",
				"sequence_order":   2,
				"can_run_parallel": true,
				"dependencies":     []string{"ARCH-01"},
			},
		},
	}

	out, err := a.CallFunction(SubmitBacklogToolName, args)
	if err != nil {
		t.Fatalf("CallFunction: %v", err)
	}
	if !strings.Contains(string(out), `"status":"success"`) && !strings.Contains(string(out), `"status": "success"`) {
		t.Fatalf("ожидался успешный статус, получено: %s", out)
	}

	ctx := context.Background()
	epics, err := a.Store.ListEpics(ctx)
	if err != nil {
		t.Fatalf("ListEpics: %v", err)
	}
	if len(epics) != 2 {
		t.Fatalf("на доске %d эпиков, ожидалось 2", len(epics))
	}
	// Сводка архитектуры сохранилась в каждом эпике.
	for _, e := range epics {
		if e.Summary != "Общее архитектурное решение" {
			t.Errorf("эпик %s: architecture_summary = %q", e.TaskID, e.Summary)
		}
		if e.ProjectName != "testproj" || e.Status != board.StatusNew {
			t.Errorf("эпик %s: project=%s status=%s", e.TaskID, e.ProjectName, e.Status)
		}
	}

	// Повторный вызов идемпотентен: дубликаты пропускаются, ошибки нет.
	if _, err := a.CallFunction(SubmitBacklogToolName, args); err != nil {
		t.Fatalf("повторный submitBacklog: %v", err)
	}
	epics, _ = a.Store.ListEpics(ctx)
	if len(epics) != 2 {
		t.Fatalf("после повторного вызова эпиков %d, ожидалось 2", len(epics))
	}
}

func TestSubmitBacklogRejectsBadSchema(t *testing.T) {
	a := newTestArchitect(t)

	// Задача без task_id — CreateEpic вернёт ошибку валидации (JSON-ответ,
	// чтобы модель могла исправиться).
	out, err := a.CallFunction(SubmitBacklogToolName, map[string]any{
		"architecture_summary": "x",
		"tasks": []map[string]any{
			{"title": "Без ID", "assigned_role": "Backend Lead"},
		},
	})
	if err != nil {
		t.Fatalf("ожидался JSON-результат с ошибкой, получили ошибку: %v", err)
	}
	if !strings.Contains(string(out), `"error"`) && !strings.Contains(string(out), "error") {
		t.Fatalf("ожидался статус error, получено: %s", out)
	}

	// Пустой tasks — ошибка формата.
	if _, err := a.CallFunction(SubmitBacklogToolName, map[string]any{
		"architecture_summary": "x",
		"tasks":                []any{},
	}); err != nil {
		t.Fatalf("ожидался JSON-результат с ошибкой, получили ошибку: %v", err)
	}

	// Неизвестный инструмент — ошибка на стороне агента.
	if _, err := a.CallFunction("WriteFiles", map[string]any{
		"files": []map[string]string{{"filename": "x", "content": "y"}},
	}); err == nil {
		t.Fatal("архитектор не должен уметь вызывать WriteFiles")
	}
}

// fakeArchitectSearcher — управляемая реализация tools.RAGSearcher для тестов
// без сети: возвращает заданные результаты поиска, запоминает параметры.
type fakeArchitectSearcher struct {
	results []rag.SearchResult
	err     error
	last    rag.SearchParams
}

func (f *fakeArchitectSearcher) Ping(_ context.Context) error { return nil }

func (f *fakeArchitectSearcher) Search(_ context.Context, p rag.SearchParams) ([]rag.SearchResult, error) {
	f.last = p
	return f.results, f.err
}

func (f *fakeArchitectSearcher) ProjectInfo(_ context.Context, _ string) (rag.ProjectInfo, error) {
	return rag.ProjectInfo{}, nil
}

// TestArchitectPromptMentionsRAGWorkflow — Ф-1: промпт архитектора велит
// изучать проект через CodeSearch/RAG и при skipped-выдаче проверять
// статус индекса инструментом RagIndexStatus (обе секции: проектирование и
// экспертиза багов).
func TestArchitectPromptMentionsRAGWorkflow(t *testing.T) {
	a := newTestArchitect(t)
	p := a.GetSystemMessages(nil)[0].Message
	for _, want := range []string{"CodeSearch", "RagIndexStatus", "RAG"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q:\n%s", want, p)
		}
	}
	bug := a.AsBugExpert().GetSystemMessages(nil)[0].Message
	for _, want := range []string{"CodeSearch", "RagIndexStatus"} {
		if !strings.Contains(bug, want) {
			t.Errorf("промпт экспертизы не содержит %q:\n%s", want, bug)
		}
	}
}

// TestArchitectPromptMentionsStackAndDepth — Ф-2/Ф-3: промпт архитектора велит
// определять стек/роли через DetectStack, назначать эпики только нужным
// лидам, учитывать глубину декомпозиции (слои) и исследовать смежный
// функционал (затрагиваемых модулей), а не штамповать «всегда Go+React+4
// лида».
func TestArchitectPromptMentionsStackAndDepth(t *testing.T) {
	a := newTestArchitect(t)
	p := a.GetSystemMessages(nil)[0].Message
	for _, want := range []string{"DetectStack", "слои", "затрагиваемых", "KISS"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q:\n%s", want, p)
		}
	}
	// Ф-3: фразы глубины/смежного/уточнения стека на месте.
	for _, want := range []string{"AskUser", "CodeSearch", "BoardGetEpic"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q:\n%s", want, p)
		}
	}
	// Жёсткий штамп убран: стек берётся из кода проекта.
	for _, disallowed := range []string{
		"Backend: Golang. Frontend: React.",
		"всегда Go + React",
	} {
		if strings.Contains(p, disallowed) {
			t.Errorf("промпт не должен содержать жёсткий штамп %q:\n%s", disallowed, p)
		}
	}
	// Инструмент обнаруживается в наборе.
	names := map[string]bool{}
	for _, td := range a.GetTools() {
		names[td.Name] = true
	}
	if !names[tools.DetectStack] {
		t.Error("архитектор не включает инструмент DetectStack")
	}
}

// TestArchitectPromptCorrectnessAndPatterns — Ф-4: промпт архитектора велит
// спрашивать пользователя (AskUser) при противоречивом/невыполнимом ТЗ ДО
// публикации бэклога (иначе — явные допущения в architecture_summary) и
// содержит секцию «Паттерны»: REST/12-factor/KISS, анти-GraphQL/devcontainer.
func TestArchitectPromptCorrectnessAndPatterns(t *testing.T) {
	a := newTestArchitect(t)
	p := a.GetSystemMessages(nil)[0].Message
	for _, want := range []string{
		"AskUser", "противоречиво", "recommended=true", "допущения",
		"ПАТТЕРНЫ", "12-factor", "GraphQL", "devcontainer", "KISS",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q:\n%s", want, p)
		}
	}
}

// TestBugExpertAndReviewPromptsMentionAskUser — Ф-4/Ф-8: режимы экспертизы
// багрепортов и ревизии черновиков тоже умеют уточнять ТЗ (AskUser) до
// вердикта/правки, иначе фиксируют допущения.
func TestBugExpertAndReviewPromptsMentionAskUser(t *testing.T) {
	a := newTestArchitect(t)
	bug := a.AsBugExpert().GetSystemMessages(nil)[0].Message
	if !strings.Contains(bug, "AskUser") || !strings.Contains(bug, "допущения") {
		t.Errorf("промпт экспертизы багов не упоминает AskUser/допущения:\n%s", bug)
	}
	r := a.AsReviewer().GetSystemMessages(nil)[0].Message
	if !strings.Contains(r, "AskUser") || !strings.Contains(r, "допущения") {
		t.Errorf("промпт ревизии черновиков не упоминает AskUser/допущения:\n%s", r)
	}
}

// TestArchitectSystemMessagesIncludeRAGBlock — Ф-1: при подключённом RAG
// (SetRAG) в системный промпт попадает блок «Релевантный код по задаче»
// (поиск — по проекту из OutputDir, scope пуст).
func TestArchitectSystemMessagesIncludeRAGBlock(t *testing.T) {
	fake := &fakeArchitectSearcher{results: []rag.SearchResult{
		{File: "server/api.go", StartLine: 1, EndLine: 3, Score: 0.9, Snippet: "package server"},
	}}
	a := newTestArchitect(t)
	a.Prompt = "спроектировать API для клиента"
	a.SetRAG(fake)

	msgs := a.GetSystemMessages(nil)
	if len(msgs) != 1 {
		t.Fatalf("ожидался 1 системный промпт, got %d", len(msgs))
	}
	p := msgs[0].Message
	if !strings.Contains(p, "Релевантный код по задаче") || !strings.Contains(p, "server/api.go") {
		t.Fatalf("промпт не содержит RAG-блок:\n%s", p)
	}
	if fake.last.Project != "testproj" {
		t.Fatalf("поиск по проекту = %q, ожидался testproj (из OutputDir)", fake.last.Project)
	}
	if fake.last.Scope != "" {
		t.Fatalf("scope должен быть пустым (весь проект), got %q", fake.last.Scope)
	}
}

// TestArchitectSystemMessagesWithoutRAG — Ф-1: nil-клиент RAG не ломает
// архитектора: промпт строится без блока «релевантный код».
func TestArchitectSystemMessagesWithoutRAG(t *testing.T) {
	a := newTestArchitect(t)
	msgs := a.GetSystemMessages(nil)
	p := msgs[0].Message
	if strings.Contains(p, "Релевантный код по задаче") {
		t.Fatalf("без RAG промпт не должен содержать блок:\n%s", p)
	}
	if !strings.Contains(p, "Системный архитектор") {
		t.Fatalf("системный промпт архитектора должен сохраниться:\n%s", p)
	}
}

// TestArchitectRAGBlockSearchParams — блок ищет по проекту, scope пуст (весь
// проект); ошибка поиска деградирует в пустую строку (как у ассистента).
func TestArchitectRAGBlockSearchParams(t *testing.T) {
	fake := &fakeArchitectSearcher{results: []rag.SearchResult{{File: "a.go", Snippet: "x"}}}
	blk := architectRAGBlock("proj-x", "где токен?", fake)
	if blk == "" {
		t.Fatal("блок должен быть непустым")
	}
	if fake.last.Scope != "" {
		t.Fatalf("scope должен быть пустым (весь проект), got %q", fake.last.Scope)
	}
	if fake.last.Project != "proj-x" {
		t.Fatalf("поиск по проекту = %q, ожидался proj-x", fake.last.Project)
	}

	// Ошибка поиска (Qdrant недоступен) → блок пуст (degrade).
	fail := &fakeArchitectSearcher{err: &rag.UnavailableError{Err: context.Canceled}}
	if got := architectRAGBlock("proj-x", "где токен?", fail); got != "" {
		t.Fatalf("ошибка поиска должна деградировать в пустой блок, got %q", got)
	}

	// nil-поисковик → пустой блок.
	if got := architectRAGBlock("proj-x", "где токен?", nil); got != "" {
		t.Fatalf("nil-поисковик должен давать пустой блок, got %q", got)
	}
}

// TestProjectNameFromOutputDir — имя проекта выводится из OutputDir (temp/<имя>);
// пустой путь — пустая строка.
func TestProjectNameFromOutputDir(t *testing.T) {
	for _, tc := range []struct {
		dir, want string
	}{
		{"/tmp/temp/my-app", "my-app"},
		{"", ""},
		{"   ", ""},
		{"/tmp/temp/my-app/", "my-app"},
	} {
		if got := projectNameFromOutputDir(tc.dir); got != tc.want {
			t.Errorf("projectNameFromOutputDir(%q) = %q, ожидался %q", tc.dir, got, tc.want)
		}
	}
}

// TestSubmitBacklogSchemaHasOpportunities — Ф-6: схема submit_architecture_backlog
// объявляет опциональное поле opportunities (массив {target_role, suggestion}).
func TestSubmitBacklogSchemaHasOpportunities(t *testing.T) {
	td := defByName(newTestArchitect(t).GetTools(), SubmitBacklogToolName)
	if td == nil {
		t.Fatal("не найдено определение submit_architecture_backlog")
	}
	props := td.Parameters["properties"].(map[string]any)
	opps, ok := props["opportunities"].(map[string]any)
	if !ok {
		t.Fatal("схема не содержит поле opportunities")
	}
	if opps["type"] != "array" {
		t.Fatalf("opportunities.type = %v, ожидался array", opps["type"])
	}
	items := opps["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	for _, field := range []string{"target_role", "suggestion"} {
		if _, ok := itemProps[field]; !ok {
			t.Errorf("схема opportunity не содержит поле %q", field)
		}
	}
	if req, ok := items["required"].([]string); !ok || len(req) != 2 {
		t.Errorf("opportunity.required = %v, ожидались target_role и suggestion", items["required"])
	}
}

// TestSubmitBacklogWithOpportunities — Ф-6: кросс-функциональные возможности
// складываются в Summary эпиков (после architecture_summary) и учитываются в
// JSON-результате; совместимость: грязные записи (без обязательных полей)
// деградируют в skipped_opportunities, а не валят бэклог.
func TestSubmitBacklogWithOpportunities(t *testing.T) {
	a := newTestArchitect(t)

	args := map[string]any{
		"architecture_summary": "Сводка решения",
		"tasks": []map[string]any{
			{
				"task_id":          "ARCH-01",
				"title":            "Backend модуль",
				"description":      "Описание",
				"assigned_role":    "Backend Lead",
				"sequence_order":   1,
				"can_run_parallel": true,
				"dependencies":     []string{},
			},
		},
		"opportunities": []map[string]any{
			{"target_role": "QA Lead", "suggestion": "Завести смоук-тесты на контракт"},
			{"target_role": "DevOps Lead", "suggestion": "Подготовить канарейку"},
			{"target_role": "Frontend Lead", "suggestion": ""}, // без suggestion — skipped
		},
	}

	out, err := a.CallFunction(SubmitBacklogToolName, args)
	if err != nil {
		t.Fatalf("CallFunction: %v", err)
	}
	if !strings.Contains(string(out), `"skipped_opportunities":1`) &&
		!strings.Contains(string(out), `"skipped_opportunities": 1`) {
		t.Fatalf("ожидался skipped_opportunities=1, получено: %s", out)
	}

	epics, err := a.Store.ListEpics(context.Background())
	if err != nil {
		t.Fatalf("ListEpics: %v", err)
	}
	if len(epics) != 1 {
		t.Fatalf("на доске %d эпиков, ожидался 1", len(epics))
	}
	s := epics[0].Summary
	if !strings.Contains(s, "Сводка решения") {
		t.Errorf("Summary потеряла architecture_summary: %q", s)
	}
	for _, want := range []string{
		"КРОСС-ФУНКЦИОНАЛЬНЫЕ ВОЗМОЖНОСТИ",
		"QA Lead: Завести смоук-тесты на контракт",
		"DevOps Lead: Подготовить канарейку",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Summary не содержит %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Frontend Lead") {
		t.Errorf("невалидная возможность (пустой suggestion) попала в Summary:\n%s", s)
	}
}

// TestArchitectPromptMentionsOpportunities — Ф-6: промпты (основной и режим
// экспертизы багов) упоминают кросс-функциональные возможности (opportunities).
func TestArchitectPromptMentionsOpportunities(t *testing.T) {
	a := newTestArchitect(t)
	p := a.GetSystemMessages(nil)[0].Message
	if !strings.Contains(p, "opportunities") || !strings.Contains(p, "КРОСС-ФУНКЦИОНАЛЬНЫЕ ВОЗМОЖНОСТИ") {
		t.Errorf("промпт не упоминает opportunities:\n%s", p)
	}
	bug := a.AsBugExpert().GetSystemMessages(nil)[0].Message
	if !strings.Contains(bug, "opportunities") {
		t.Errorf("промпт экспертизы багов не упоминает opportunities:\n%s", bug)
	}
}
