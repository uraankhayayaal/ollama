package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai/agents"
	"ai/agents/chatassist"
	"ai/board"
	"ai/chat"
	"ai/runner"
	"ai/workspace"
)

// qaStubProvider — заглушка LLM-провайдера для runChatAssist.
type qaStubProvider struct {
	called   chan struct{}
	gotAgent agents.Agent
	resp     *runner.AgentResponse
	err      error
}

func (p *qaStubProvider) Generate(_ context.Context, a agents.Agent) (*runner.AgentResponse, error) {
	p.gotAgent = a
	close(p.called)
	return p.resp, p.err
}

// registerTestDir регистрирует временный каталог как проект KindDir.
func registerTestDir(t *testing.T, srv *Server, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.reg.Add(workspace.AddParams{Name: name, Kind: workspace.KindDir, Root: dir, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// waitChatRole ждёт появления сообщения указанной роли в истории чата.
func waitChatRole(t *testing.T, sess *Session, role chat.Role, within time.Duration) chat.Message {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		hist, err := sess.chat.History(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range hist {
			if m.Role == role {
				return m
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("нет сообщения роли %q в истории за %v", role, within)
	return chat.Message{}
}

func TestIsChatQuestion(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"Что сейчас делает проект?", true},
		{"что", true},
		{"какой текущий статус задачи X", true},
		{"Статус отчёта", true},
		{"Сколько задач в работе", true},
		{"Расскажи про архитектуру", true},
		{"Объясни, как работает main", true},
		{"Покажи структуру проекта", true},
		{"есть ли баги", true},
		{"На каком этапе остановились?", true},
		{"Где лежит конфиг", true},
		{"Оптимизируй загрузку страницы", false},
		{"Сделай файл readme", false},
		{"Проверь, что все тесты проходят", false},
		{"Добавь статусы в отчёт и сохрани", false},
		{"", false},
		{"   ", false},
		{"123", false},
	}
	for _, c := range cases {
		if got := isChatQuestion(c.msg); got != c.want {
			t.Errorf("isChatQuestion(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func TestChatAssistAnswersQuestion(t *testing.T) {
	srv, _, _ := newTestServer(t)
	dir := registerTestDir(t, srv, "proj-qa")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, _, err := srv.getOrCreate("proj-qa")
	if err != nil {
		t.Fatal(err)
	}

	prov := &qaStubProvider{called: make(chan struct{}), resp: &runner.AgentResponse{Content: "В работе: эпик «Логин». Задач в работе: 1."}}
	sess.runChatAssist(context.Background(), "что делает проект?", prov)

	m := waitChatRole(t, sess, chat.RoleAssistant, 3*time.Second)
	if m.Content != "В работе: эпик «Логин». Задач в работе: 1." {
		t.Fatalf("content = %q", m.Content)
	}
	if m.Agent != "assistant" {
		t.Fatalf("agent = %q, want assistant", m.Agent)
	}
	if _, ok := prov.gotAgent.(*chatassist.Assistant); !ok {
		t.Fatalf("провайдер получил агента %T, ожидается *chatassist.Assistant", prov.gotAgent)
	}
}

func TestChatAssistEmptyAnswerFallback(t *testing.T) {
	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-qa-empty")
	sess, _, err := srv.getOrCreate("proj-qa-empty")
	if err != nil {
		t.Fatal(err)
	}

	prov := &qaStubProvider{called: make(chan struct{}), resp: &runner.AgentResponse{Content: "  "}}
	sess.runChatAssist(context.Background(), "как дела?", prov)

	m := waitChatRole(t, sess, chat.RoleAssistant, 3*time.Second)
	if !strings.Contains(m.Content, "Не расслышал") {
		t.Fatalf("content = %q, ожидалась заглушка «Не расслышал»", m.Content)
	}
}

func TestChatAssistErrorAppendsStatus(t *testing.T) {
	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-qa-err")
	sess, _, err := srv.getOrCreate("proj-qa-err")
	if err != nil {
		t.Fatal(err)
	}

	prov := &qaStubProvider{called: make(chan struct{}), err: errors.New("сбой модели")}
	sess.runChatAssist(context.Background(), "что делает проект?", prov)

	m := waitChatRole(t, sess, chat.RoleStatus, 3*time.Second)
	if !strings.Contains(m.Content, "Ошибка") || !strings.Contains(m.Content, "сбой модели") {
		t.Fatalf("content = %q, ожидается статус с текстом ошибки", m.Content)
	}
}

func TestChatAssistPromptHasBoardContext(t *testing.T) {
	srv, _, mr := newTestServer(t)
	registerTestDir(t, srv, "proj-qa-board")

	// Публикуем эпики на доску через store на том же Redis.
	store := board.NewStoreNoCheck(board.StoreConfig{Addr: mr.Addr(), Project: "proj-qa-board"})
	ctx := context.Background()
	store.CreateEpic(ctx, &board.Epic{TaskSpec: board.TaskSpec{TaskID: "epic-a", Title: "Логин"}})
	store.CreateTask(ctx, &board.Task{TaskSpec: board.TaskSpec{TaskID: "task-1", Title: "Форма входа", AssignedRole: "Dev"}, EpicID: "epic-a", Status: board.StatusInProgress})

	sess, _, err := srv.getOrCreate("proj-qa-board")
	if err != nil {
		t.Fatal(err)
	}

	p := sess.chatAssistPrompt("сколько задач в работе?")
	for _, want := range []string{
		"Вопрос пользователя", "сколько задач в работе",
		"Эпики:", "Логин",
		"В работе сейчас:", "Форма входа",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("промпт не содержит %q:\n%s", want, p)
		}
	}
}
