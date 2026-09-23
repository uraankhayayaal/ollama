package chatassist

import (
	"ai/agents"
	"ai/board"
	"ai/rag"
	"ai/tools"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// newTestAssistant создаёт ассистента во временной директории без доски и RAG.
func newTestAssistant(t *testing.T, prompt string) *Assistant {
	t.Helper()
	return newAssistant(filepath.Join(t.TempDir(), "proj"), "testproj", prompt, nil, nil)
}

// newTestAssistantBoard создаёт ассистента с подключённой in-memory доской.
// Доску возвращаем для контроля состояния.
func newTestAssistantBoard(t *testing.T, prompt string) (*Assistant, *board.Store) {
	t.Helper()
	srv := miniredis.RunT(t)
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: srv.Addr(), Project: "testproj"})
	a := newAssistant(filepath.Join(t.TempDir(), "proj"), "testproj", prompt, store, nil)
	return a, store
}

// fakeAssistantSearcher — управляемая реализация поисковика для тестов без сети.
type fakeAssistantSearcher struct {
	results []rag.SearchResult
	err     error
	last    rag.SearchParams
}

func (f *fakeAssistantSearcher) Ping(context.Context) error { return nil }

func (f *fakeAssistantSearcher) Search(_ context.Context, p rag.SearchParams) ([]rag.SearchResult, error) {
	f.last = p
	return f.results, f.err
}

func (f *fakeAssistantSearcher) ProjectInfo(_ context.Context, _ string) (rag.ProjectInfo, error) {
	return rag.ProjectInfo{}, nil
}

var _ agents.Agent = (*Assistant)(nil)

func TestAssistantInterface(t *testing.T) {
	a := newTestAssistant(t, "что сейчас делает проект?")
	if len(a.GetSystemMessages(nil)) == 0 {
		t.Fatal("системный промпт не задан")
	}
	um := a.GetUserMessages()
	if len(um) != 1 || um[0].Type != agents.MessageTypeHuman {
		t.Fatalf("ожидается один human-промпт с вопросом, получено %+v", um)
	}
	if msg, must := a.RequiredToolFirstRound(); must {
		t.Fatalf("ассистент не обязан вызывать инструмент, got %q", msg)
	}
}

// TestAssistantToolsIncludeBoardWritesAndCodeSearch — набор ассистента содержит
// чтение файлов, семантический поиск по коду (CodeSearch) и write-инструменты
// доски (создание эпиков/багов), чтобы ассистент мог действовать по смыслу
// сообщения. Пишущих файловых инструментов быть не должно (Ф-1).
func TestAssistantToolsIncludeBoardWritesAndCodeSearch(t *testing.T) {
	a, _ := newTestAssistantBoard(t, "создай эпик порт на Rust")
	names := map[string]bool{}
	for _, td := range a.GetTools() {
		names[td.Name] = true
	}
	for _, w := range []string{
		"List", "ReadFiles", "ReadMap",
		tools.CodeSearch,
		tools.WebSearch,
		tools.BoardListEpics, tools.BoardGetEpic, tools.BoardListTasks, tools.BoardGetTask,
		tools.BoardListBugs, tools.BoardGetBug,
		tools.BoardCreateEpic, tools.BoardUpdateEpic, tools.BoardDeleteEpic, tools.BoardSetEpicStatus,
		tools.BoardSetTaskStatus,
		tools.BoardCreateBug, tools.BoardSetBugStatus, tools.BoardReviewBug,
	} {
		if !names[w] {
			t.Errorf("ассистент не включает инструмент %q", w)
		}
	}
	// Файлы проекта ассистент не пишет — за разработку отвечают агенты.
	for _, w := range []string{"WriteFiles", "AppendFile", "DeleteFiles", "Run", "SearchReplace"} {
		if names[w] {
			t.Errorf("ассистент не должен включать инструмент %q", w)
		}
	}
	// Задачи внутри эпика ассистент не создаёт/не правит (Ф-8): их заводят лиды
	// направлений, а все чат-эпики проходят обязательную ревизию архитектора.
	for _, w := range []string{"BoardCreateTask", "BoardUpdateTask", "BoardDeleteTask"} {
		if names[w] {
			t.Errorf("ассистент не должен включать task-write инструмент %q", w)
		}
	}
}

// TestAssistantWithoutBoardSkipsBoardTools — без доски (store == nil) Board-инструменты
// не попадают в набор: их вызов вернул бы «доска не подключена».
func TestAssistantWithoutBoardSkipsBoardTools(t *testing.T) {
	a := newTestAssistant(t, "вопрос")
	names := map[string]bool{}
	for _, td := range a.GetTools() {
		names[td.Name] = true
	}
	for _, w := range []string{"BoardListEpics", "BoardCreateEpic", "BoardSetTaskStatus"} {
		if names[w] {
			t.Errorf("ассистент без доски не должен включать Board-инструмент %q", w)
		}
	}
}

func TestAssistantCallFunctionRejectsWrite(t *testing.T) {
	a := newTestAssistant(t, "вопрос")
	if _, err := a.CallFunction("WriteFiles", nil); err == nil {
		t.Fatal("попытка вызвать записывающий файл инструмент должна завершиться ошибкой (not in tool set)")
	}
}

func TestAssistantWritesIntoOutputDir(t *testing.T) {
	// sanity: ассистент остаётся FileOps-агентом, способным читать проект.
	a := newTestAssistant(t, "покажи структуру")
	if err := os.WriteFile(filepath.Join(a.OutputDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := a.CallFunction("List", map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("List не вернул содержимое проекта")
	}
}

// TestAssistantCreatesEpicOnBoardDirectly — ассистент сам выполняет действие на
// доске через свой набор: BoardCreateEpic доступен и реально создаёт эпик.
func TestAssistantCreatesEpicOnBoardDirectly(t *testing.T) {
	a, store := newTestAssistantBoard(t, "создай эпик")
	out, err := a.CallFunction(tools.BoardCreateEpic, map[string]any{
		"task_id": "CHAT-01", "title": "Порт на Rust", "description": "перенести сервер",
		"assigned_role": "Backend Lead",
	})
	if err != nil {
		t.Fatalf("BoardCreateEpic: %v", err)
	}
	if !strings.Contains(string(out), "success") {
		t.Fatalf("ожидался success, got %s", out)
	}
	e, err := store.GetEpic(context.Background(), "CHAT-01")
	if err != nil {
		t.Fatalf("эпик не на доске: %v", err)
	}
	if e.Title != "Порт на Rust" {
		t.Fatalf("title = %q", e.Title)
	}
}

// TestAssistantSystemMessagesIncludeRAGBlock — при доступном RAG в системный
// промпт попадает блок «Релевантный код по вопросу» (поиск — по проекту).
func TestAssistantSystemMessagesIncludeRAGBlock(t *testing.T) {
	fake := &fakeAssistantSearcher{results: []rag.SearchResult{
		{File: "server/token.go", StartLine: 1, EndLine: 3, Score: 0.91, Snippet: "package server"},
	}}
	a := newAssistant(filepath.Join(t.TempDir(), "proj"), "proj-x", "где валидация токена?", nil, fake)
	msgs := a.GetSystemMessages(nil)
	if len(msgs) != 1 {
		t.Fatalf("ожидался 1 системный промпт, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Message, "Релевантный код по вопросу") || !strings.Contains(msgs[0].Message, "server/token.go") {
		t.Fatalf("промпт не содержит RAG-блок:\n%s", msgs[0].Message)
	}
	if fake.last.Project != "proj-x" {
		t.Fatalf("поиск по проекту = %q, ожидался proj-x", fake.last.Project)
	}
}

// TestAssistantSystemMessagesWithoutRAG — nil-клиент RAG (или пустой проект) не
// ломает ассистента: промпт строится без блока «релевантный код».
func TestAssistantSystemMessagesWithoutRAG(t *testing.T) {
	a := newAssistant(filepath.Join(t.TempDir(), "proj"), "proj-x", "где валидация?", nil, nil)
	msgs := a.GetSystemMessages(nil)
	if strings.Contains(msgs[0].Message, "Релевантный код по вопросу") {
		t.Fatalf("без RAG промпт не должен содержать блок:\n%s", msgs[0].Message)
	}
	if !strings.Contains(msgs[0].Message, "ассистент") {
		t.Fatalf("системный промпт ассистента должен сохраниться:\n%s", msgs[0].Message)
	}
}

// TestAssistantPromptHasWebSearchRule — системный промпт содержит правило
// живой информации: свежие данные (новости/факты/справка/погода) — через
// WebSearch, degraded — честно ответить из знаний с пометкой «не живые».
func TestAssistantPromptHasWebSearchRule(t *testing.T) {
	a := newTestAssistant(t, "какая погода в Москве?")
	msgs := a.GetSystemMessages(nil)
	p := msgs[0].Message
	for _, want := range []string{"WebSearch", "degraded", "не живые", "новости", "погода"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q:\n%s", want, p)
		}
	}
}

// TestAssistantPromptHasCreationChecklist — при создании эпиков промпт требует
// собрать СВОДКУ обязательных параметров, при неоднозначности СПРОСИТЬ
// пользователя (а не угадывать) и вывести пример сводки перед вызовом
// инструмента; чат-эпик позиционируется как черновик для Системного
// архитектора, задачи напрямую не заводятся (Ф-8).
func TestAssistantPromptHasCreationChecklist(t *testing.T) {
	a := newTestAssistant(t, "создай эпик на рефакторинг бэкенда")
	p := a.GetSystemMessages(nil)[0].Message
	for _, want := range []string{"сводк", "спроси", "BoardCreateEpic", "Пример сводки", "черновик для Системного архитектора"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q:\n%s", want, p)
		}
	}
	for _, forbid := range []string{"СВОДКА ДЛЯ ЗАДАЧИ", "BoardCreateTask", "ревью архитектора: не требуется"} {
		if strings.Contains(p, forbid) {
			t.Errorf("промпт не должен содержать %q:\n%s", forbid, p)
		}
	}
}

// TestAssistantRAGBlockSearchParams — блок ищет по проекту, scope пуст (весь
// проект); ошибка поиска деградирует в пустую строку.
func TestAssistantRAGBlockSearchParams(t *testing.T) {
	fake := &fakeAssistantSearcher{results: []rag.SearchResult{{File: "a.go", Snippet: "x"}}}
	blk := assistantRAGBlock("proj-x", "где токен?", fake)
	if blk == "" {
		t.Fatal("блок должен быть непустым")
	}
	if fake.last.Scope != "" {
		t.Fatalf("scope должен быть пустым (весь проект), got %q", fake.last.Scope)
	}

	// Ошибка поиска (Qdrant недоступен) → блок пуст (degrade, как планировщик).
	fail := &fakeAssistantSearcher{err: &rag.UnavailableError{Err: context.Canceled}}
	if got := assistantRAGBlock("proj-x", "где токен?", fail); got != "" {
		t.Fatalf("ошибка поиска должна деградировать в пустой блок, got %q", got)
	}

	// nil-поисковик → пустой блок.
	if got := assistantRAGBlock("proj-x", "где токен?", nil); got != "" {
		t.Fatalf("nil-поисковик должен давать пустой блок, got %q", got)
	}
}
