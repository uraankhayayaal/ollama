package chatassist

import (
	"ai/agents"
	"ai/infrastructure/rag"
	"ai/tools"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestAssistant создаёт агента во временной директории без доски и RAG.
func newTestAssistant(t *testing.T, prompt string) *Assistant {
	t.Helper()
	return newAssistant(filepath.Join(t.TempDir(), "proj"), "testproj", prompt, nil)
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

// TestAssistantCorrectionAppendedToSystemPrompt — Correction (разовая
// корректирующая инструкция сервера на повторный ход) попадает в конец
// системного промпта.
func TestAssistantCorrectionAppendedToSystemPrompt(t *testing.T) {
	a := newTestAssistant(t, "заведи эпик")
	if strings.Contains(a.GetSystemMessages(nil)[0].Message, "ИСПРАВЛЕНИЕ-TEST") {
		t.Fatal("пустая Correction не должна влиять на промпт")
	}
	a.Correction = "ИСПРАВЛЕНИЕ-TEST: обязательно вызови инструмент"
	if !strings.Contains(a.GetSystemMessages(nil)[0].Message, "ИСПРАВЛЕНИЕ-TEST") {
		t.Fatal("промпт не содержит корректирующую инструкцию")
	}
}

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
		t.Fatalf("агент не обязан вызывать инструмент, got %q", msg)
	}
}

// TestAssistantToolsIncludeAllTools — набор агента содержит чтение,
// правку, навигацию и проверки (Ф-1).
func TestAssistantToolsIncludeAllTools(t *testing.T) {
	a := newTestAssistant(t, "создай сервис")
	names := map[string]bool{}
	for _, td := range a.GetTools() {
		names[td.Name] = true
	}
	for _, w := range []string{
		"List", "ReadFiles", "ReadMap",
		tools.CodeSearch,
		tools.WebSearch,
		"WriteFiles", "AppendFile", "DeleteFiles",
		"SearchReplace",
		"Run",
	} {
		if !names[w] {
			t.Errorf("агент не включает инструмент %q", w)
		}
	}
	// Board-инструменты не регистрируются без store.
	for _, w := range []string{"BoardCreateTask", "BoardUpdateTask", "BoardDeleteTask"} {
		if names[w] {
			t.Errorf("агент без доски не должен включать Board-инструмент %q", w)
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
			t.Errorf("агент без доски не должен включать Board-инструмент %q", w)
		}
	}
}

// TestAssistantUnknownToolReturnsSkipped — вызов неизвестного инструмента
// возвращает status=skipped (не ошибку).
func TestAssistantUnknownToolReturnsSkipped(t *testing.T) {
	a := newTestAssistant(t, "вопрос")
	out, err := a.CallFunction("NonExistentTool", map[string]any{})
	if err != nil {
		t.Fatalf("не должно быть ошибки, got %v", err)
	}
	body := string(out)
	if !strings.Contains(body, `"status":"skipped"`) {
		t.Errorf("ожидался status=skipped, got %s", body)
	}
}

// TestAssistantRepeatsIdenticalCallBlocked — тот же вызов того же инструмента с
// теми же аргументами допускается limitedToolRepeats раз, дальше не
// выполняется (status=skipped).
func TestAssistantRepeatsIdenticalCallBlocked(t *testing.T) {
	a := newTestAssistant(t, "удали упоминания rust")
	args := map[string]any{"filenames": []string{".github/workflows/ci.yml"}}

	for i := 1; i <= limitedToolRepeats; i++ {
		out, err := a.CallFunction("ReadFiles", args)
		if err != nil {
			t.Fatalf("вызов %d: %v", i, err)
		}
		if strings.Contains(string(out), "уже вызван") {
			t.Fatalf("вызов %d не должен блокироваться (лимит %d)", i, limitedToolRepeats)
		}
	}
	out, err := a.CallFunction("ReadFiles", args)
	if err != nil {
		t.Fatalf("вызов %d: %v", limitedToolRepeats+1, err)
	}
	body := string(out)
	if !strings.Contains(body, `"status":"skipped"`) {
		t.Fatalf("повторный вызов должен быть заблокирован, got %s", body)
	}
	if !strings.Contains(body, "уже вызван") {
		t.Errorf("в ответе должно быть объяснение о повторе:\n%s", body)
	}
	// Разные аргументы — законный сценарий и блокироваться не должен.
	out, err = a.CallFunction("ReadFiles", map[string]any{"filenames": []string{"README.md"}})
	if err != nil {
		t.Fatalf("другой файл: %v", err)
	}
	if strings.Contains(string(out), "уже вызван") {
		t.Errorf("вызов с другими аргументами не должен блокироваться:\n%s", out)
	}
}

// TestAssistantToolCallSignatureIgnoresKeyOrder — порядок ключей в аргументах
// не должен обходить антизацикливание: модель может переставлять поля, но
// вызов по смыслу тот же.
func TestAssistantToolCallSignatureIgnoresKeyOrder(t *testing.T) {
	sig := func(args map[string]any) string { return toolCallSignature("ReadFiles", args) }
	a1 := map[string]any{"filenames": []string{"a.go"}, "lines": "1-10"}
	a2 := map[string]any{"lines": "1-10", "filenames": []string{"a.go"}}
	if sig(a1) != sig(a2) {
		t.Fatalf("подписи различаются при перестановке ключей:\n%s\n%s", sig(a1), sig(a2))
	}
}

// TestAssistantPromptHasWorkRules — промпт содержит правила работы с кодом:
// изучай → ТОЧЕЧНЫЕ изменения → проверки → итог.
func TestAssistantPromptHasWorkRules(t *testing.T) {
	a := newTestAssistant(t, "пофикси баг")
	p := a.GetSystemMessages(nil)[0].Message
	for _, want := range []string{
		"Изучай текущее состояние проекта перед правками",
		"ТОЧЕЧНЫЕ изменения",
		"ЗАВЕРШАЙ задачу в одном цикле",
		"НЕ ПОВТОРЯЙ ОДИН И ТОТ ЖЕ ВЫЗОВ",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q", want)
		}
	}
}

func TestAssistantWritesIntoOutputDir(t *testing.T) {
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

// TestAssistantSystemMessagesIncludeRAGBlock — при доступном RAG в системный
// промпт попадает блок «Релевантный код по вопросу» (поиск — по проекту).
func TestAssistantSystemMessagesIncludeRAGBlock(t *testing.T) {
	fake := &fakeAssistantSearcher{results: []rag.SearchResult{
		{File: "server/token.go", StartLine: 1, EndLine: 3, Score: 0.91, Snippet: "package server"},
	}}
	a := newAssistant(filepath.Join(t.TempDir(), "proj"), "proj-x", "где валидация токена?", fake)
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
// ломает агента: промпт строится без блока «релевантный код».
func TestAssistantSystemMessagesWithoutRAG(t *testing.T) {
	a := newAssistant(filepath.Join(t.TempDir(), "proj"), "proj-x", "где валидация?", nil)
	msgs := a.GetSystemMessages(nil)
	if strings.Contains(msgs[0].Message, "Релевантный код по вопросу") {
		t.Fatalf("без RAG промпт не должен содержать блок:\n%s", msgs[0].Message)
	}
	if !strings.Contains(msgs[0].Message, "ассистент") {
		t.Fatalf("системный промпт агента должен сохраниться:\n%s", msgs[0].Message)
	}
}

// TestAssistantPromptHasWebSearchRule — системный промпт упоминает WebSearch
// как источник актуальной информации.
func TestAssistantPromptHasWebSearchRule(t *testing.T) {
	a := newTestAssistant(t, "какая погода в Москве?")
	msgs := a.GetSystemMessages(nil)
	p := msgs[0].Message
	for _, want := range []string{"WebSearch"} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q:\n%s", want, p)
		}
	}
}

// TestAssistantHistoryBlockDemandsMemory — блок истории обязывает модель
// сверяться с уже заданными вопросами: без этого агент переспрашивает
// подтверждение по кругу.
func TestAssistantHistoryBlockDemandsMemory(t *testing.T) {
	blk := historyPromptBlock("[пользователь] да\n[ассистент] Подтвердите?")
	if !strings.Contains(blk, "не задавай вопрос, который ты уже задавал") {
		t.Errorf("блок истории не запрещает повторный вопрос:\n%s", blk)
	}
	if !strings.Contains(blk, "не переспрашивай подтверждение") {
		t.Errorf("блок истории не запрещает переподтверждение:\n%s", blk)
	}
}

// TestAssistantPromptHasNoDestructiveBoard — промпт агента не содержит
// ссылок на удалённые Board-концепции.
func TestAssistantPromptHasNoDestructiveBoard(t *testing.T) {
	a := newTestAssistant(t, "залей в main")
	p := a.GetSystemMessages(nil)[0].Message
	for _, forbid := range []string{
		"action=status", "action=start", "action=apply",
	} {
		if strings.Contains(p, forbid) {
			t.Errorf("промпт не должен содержать %q:\n%s", forbid, p)
		}
	}
}

// TestAssistantPromptHasAllToolCategories — промпт агента содержит все категории
// инструментов: чтение, правку, проверки.
func TestAssistantPromptHasAllToolCategories(t *testing.T) {
	a := newTestAssistant(t, "хочу рефакторинг")
	p := a.GetSystemMessages(nil)[0].Message
	for _, want := range []string{
		"List", "ReadFiles", "ReadMap",
		"LspDefinition", "LspReferences", "LspHover",
		"CodeSearch", "WebSearch",
		"WriteFiles", "SearchReplace",
		"Run",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит инструмент %q", want)
		}
	}
}

// TestAssistantPromptNoBoardMentions — промпт агента не содержит
// упоминаний BA-ролей, BA-шаблонов и других удалённых концепций.
func TestAssistantPromptNoBoardMentions(t *testing.T) {
	a := newTestAssistant(t, "заведи эпик")
	p := a.GetSystemMessages(nil)[0].Message
	for _, forbid := range []string{
		"ШАБЛОН BA-АРТЕФАКТА", "BABOK", "AS-IS", "TO-BE", "GAP",
		"бизнес-аналитик",
	} {
		if strings.Contains(p, forbid) {
			t.Errorf("промпт не должен содержать %q", forbid)
		}
	}
}

// TestAssistantPromptNoAskUserMentions — промпт агента не содержит
// упоминаний AskUser (удалённый инструмент).
func TestAssistantPromptNoAskUserMentions(t *testing.T) {
	a := newTestAssistant(t, "хочу что-то улучшить")
	p := a.GetSystemMessages(nil)[0].Message
	for _, forbid := range []string{
		"AskUser", "УТОЧНЕНИЕ И ОБОГАЩЕНИЕ", "ОГРАНИЧЕНИЯ РОЛИ",
		"ОДНУ пачку структурированных вопросов",
	} {
		if strings.Contains(p, forbid) {
			t.Errorf("промпт не должен содержать %q", forbid)
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

	// Ошибка поиска (Qdrant недоступен) → блок пуст (degrade).
	fail := &fakeAssistantSearcher{err: &rag.UnavailableError{Err: context.Canceled}}
	if got := assistantRAGBlock("proj-x", "где токен?", fail); got != "" {
		t.Fatalf("ошибка поиска должна деградировать в пустой блок, got %q", got)
	}

	// nil-поисковик → пустой блок.
	if got := assistantRAGBlock("proj-x", "где токен?", nil); got != "" {
		t.Fatalf("nil-поисковик должен давать пустой блок, got %q", got)
	}
}
