package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
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
	"ai/tools"
	"ai/workspace"
)

// qaStubProvider — заглушка LLM-провайдера для runChatAssistant.

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

// scriptedChatProvider — провайдер одного раунда диалога (ChatOnce), отдаёт
// ответы по порядку (последний повторяется). Используется для прогона полного
// цикла ассистента (runner.Generate) в hermetic-тесте.
type scriptedChatProvider struct {
	replies []*runner.ModelReply
	calls   int
}

func (c *scriptedChatProvider) ChatOnce(_ context.Context, _ agents.Agent, _ []runner.Message) (*runner.ModelReply, error) {
	idx := c.calls
	if idx >= len(c.replies) {
		idx = len(c.replies) - 1
	}
	c.calls++
	if idx < 0 || idx >= len(c.replies) {
		return &runner.ModelReply{Content: "готово", FinishReason: "stop"}, nil
	}
	return c.replies[idx], nil
}

// scriptedGenerateProvider — LLMProvider-обёртка над scriptedChatProvider: гоняет
// полный агентский цикл инструментов (runner.Generate), как реальные провайдеры.
type scriptedGenerateProvider struct {
	chat *scriptedChatProvider
}

func (p *scriptedGenerateProvider) Generate(ctx context.Context, a agents.Agent) (*runner.AgentResponse, error) {
	return runner.Generate(ctx, p.chat, a)
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
	sess.runChatAssistant(context.Background(), "что делает проект?", prov)

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

// TestChatAssistHistoryInjected — ассистент получает историю диалога: прошлые
// реплики пользователя/ассистента уходят в History и в системный промпт, а
// текущий вопрос (последняя запись стрима) из истории исключается.
func TestChatAssistHistoryInjected(t *testing.T) {
	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-qa-hist")
	sess, _, err := srv.getOrCreate("proj-qa-hist")
	if err != nil {
		t.Fatal(err)
	}

	// Предыдущий диалог: вопрос пользователя и ответ ассистента.
	sess.append(chat.RoleUser, "создай эпик на аутентификацию", "user", "", nil)
	sess.append(chat.RoleAssistant, "Эпик создан: AUTH-01.", "assistant", "", nil)

	prov := &qaStubProvider{called: make(chan struct{}), resp: &runner.AgentResponse{Content: "готово"}}
	sess.runChatAssistant(context.Background(), "и добавь задачу в него", prov)
	<-prov.called

	asst, ok := prov.gotAgent.(*chatassist.Assistant)
	if !ok {
		t.Fatalf("провайдер получил агента %T, ожидается *chatassist.Assistant", prov.gotAgent)
	}
	if !strings.Contains(asst.History, "создай эпик на аутентификацию") {
		t.Fatalf("история не содержит прошлой реплики пользователя: %q", asst.History)
	}
	if !strings.Contains(asst.History, "Эпик создан: AUTH-01.") {
		t.Fatalf("история не содержит ответа ассистента: %q", asst.History)
	}
	if strings.Contains(asst.History, "и добавь задачу в него") {
		t.Fatalf("текущий вопрос не должен дублироваться в истории: %q", asst.History)
	}

	var sys strings.Builder
	for _, m := range asst.GetSystemMessages(nil) {
		sys.WriteString(m.Message)
	}
	if !strings.Contains(sys.String(), "История диалога с пользователем") {
		t.Fatalf("системный промпт не содержит блока истории диалога")
	}
	if !strings.Contains(sys.String(), "создай эпик на аутентификацию") {
		t.Fatalf("системный промпт не содержит прошлой реплики")
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
	sess.runChatAssistant(context.Background(), "как дела?", prov)

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
	sess.runChatAssistant(context.Background(), "что делает проект?", prov)

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

	p := sess.chatAssistantPrompt("сколько задач в работе?")
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

// waitBoard waits until predicates on the board are satisfied (hermetic helper
// для асинхронного runChatAssistant).
func waitBoard(t *testing.T, sess *Session, within time.Duration, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("состояние доски не наступило за", within)
}

// waitChatAssistant ждёт появления НЕПУСТОГО сообщения ассистента в истории:
// пустые assistant-сообщения с промежуточными tool_calls (транслирует
// репортёр агентского цикла) пропускаются.
func waitChatAssistant(t *testing.T, sess *Session, within time.Duration) chat.Message {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		hist, err := sess.chat.History(context.Background(), 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range hist {
			if m.Role == chat.RoleAssistant && strings.TrimSpace(m.Content) != "" {
				return m
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("нет непустого сообщения ассистента за %v", within)
	return chat.Message{}
}

// runScriptedChatAssistant запускает runChatAssistant с моделью, которая играет
// заданные раунды (tool_calls → текст), и ждёт ответ ассистента в чате.
func runScriptedChatAssistant(t *testing.T, sess *Session, question string, replies ...*runner.ModelReply) chat.Message {
	t.Helper()
	prov := &scriptedGenerateProvider{chat: &scriptedChatProvider{replies: replies}}
	sess.runChatAssistant(context.Background(), question, prov)
	return waitChatAssistant(t, sess, 3*time.Second)
}

// TestChatAssistantCreatesEpic — «создай эпик …»: модель в цикле инструментов
// вызывает BoardCreateEpic, эпик попадает на доску. Бинарный сплит убран:
// доска меняется инструментом ассистента, а не регэкспепом.
func TestChatAssistantCreatesEpic(t *testing.T) {
	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-act-epic")
	sess, _, err := srv.getOrCreate("proj-act-epic")
	if err != nil {
		t.Fatal(err)
	}

	m := runScriptedChatAssistant(t, sess, "создай эпик на порт на Rust",
		&runner.ModelReply{
			ToolCalls: []tools.ToolCall{{
				Name:      tools.BoardCreateEpic,
				Arguments: `{"task_id":"CHAT-01","title":"Порт на Rust","description":"Перевести сервер на Rust","assigned_role":"Backend Lead"}`,
			}},
			FinishReason: "tool_calls",
		},
		&runner.ModelReply{Content: "Эпик создан на доске.", FinishReason: "stop"},
	)
	if !strings.Contains(m.Content, "Эпик создан") {
		t.Fatalf("ответ ассистента = %q", m.Content)
	}

	waitBoard(t, sess, 3*time.Second, func() bool {
		epics, err := sess.board.ListEpics(context.Background())
		if err != nil {
			return false
		}
		return len(epics) == 1 && epics[0].TaskID == "CHAT-01" && epics[0].Title == "Порт на Rust"
	})
}

// TestChatAssistantPublishesBoardWhenIdle — «создай эпик …» в idle-сессии
// (оркестрация не запущена): boardFlusher в этот момент не крутится, поэтому
// router-колбэк обязан опубликовать снимок доски в шину сразу (через
// emitBoard), иначе созданный чатом эпик не появится на доске Web UI
// до ручного обновления страницы.
func TestChatAssistantPublishesBoardWhenIdle(t *testing.T) {
	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-act-pub")
	sess, _, err := srv.getOrCreate("proj-act-pub")
	if err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	sess.running = false
	sess.mu.Unlock()

	srvConn, cliConn := net.Pipe()
	defer cliConn.Close()
	ws := &WsConn{conn: srvConn, br: bufio.NewReader(srvConn), closed: make(chan struct{})}
	t.Cleanup(func() { _ = ws.Close() })
	srv.hub.Subscribe("proj-act-pub", ws)

	runScriptedChatAssistant(t, sess, "создай эпик на порт на Rust",
		&runner.ModelReply{
			ToolCalls: []tools.ToolCall{{
				Name:      tools.BoardCreateEpic,
				Arguments: `{"task_id":"CHAT-01","title":"Порт на Rust","description":"Перевести сервер на Rust","assigned_role":"Backend Lead"}`,
			}},
			FinishReason: "tool_calls",
		},
		&runner.ModelReply{Content: "Эпик создан на доске.", FinishReason: "stop"},
	)

	// Читаем кадры до первого board-события: в idle-сессии оно обязано прийти
	// сразу после инструмента (без boardFlusher). Ранние снимки (до выполнения
	// инструмента — борда ещё пуста) пропускаем, ждём с эпиком.
	var found bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, ok := readBoardSnapshot(t, cliConn, 2*time.Second)
		if !ok {
			break
		}
		if len(b.Epics) == 1 && b.Epics[0].TaskID == "CHAT-01" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("board-событие с эпиком CHAT-01 не пришло: idle-сессия должна публиковать снимок сразу")
	}
}

// readBoardSnapshot читает кадры WS до первого board-события (или таймаута) и
// возвращает его payload. ok=false — события не было.
func readBoardSnapshot(t *testing.T, conn net.Conn, timeout time.Duration) (boardSnapshot, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var ev struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		hdr := make([]byte, 2)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			continue
		}
		length := int64(hdr[1] & 0x7f)
		switch length {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(conn, ext); err != nil {
				continue
			}
			length = int64(ext[0])<<8 | int64(ext[1])
		case 127:
			ext := make([]byte, 8)
			if _, err := io.ReadFull(conn, ext); err != nil {
				continue
			}
			length = 0
			for _, b := range ext {
				length = length<<8 | int64(b)
			}
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(conn, body); err != nil {
			continue
		}
		if err := json.Unmarshal(body, &ev); err != nil {
			continue
		}
		if ev.Type != "board" {
			continue
		}
		var snap boardSnapshot
		if err := json.Unmarshal(ev.Payload, &snap); err != nil {
			continue
		}
		return snap, true
	}
	return boardSnapshot{}, false
}

// TestChatAssistantCreatesBugAndTask — «хочу канбан на рефакторинг» и «заведи
// баг про тормоза» приводят к действиям на доске (эпик+задача, баг).
func TestChatAssistantCreatesBugAndTask(t *testing.T) {
	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-act-bug")
	sess, _, err := srv.getOrCreate("proj-act-bug")
	if err != nil {
		t.Fatal(err)
	}

	// Раунд 1: канбан на рефакторинг → эпик + задача. Раунд 2: баг → баг.
	runScriptedChatAssistant(t, sess, "хочу канбан на рефакторинг сервера",
		&runner.ModelReply{
			ToolCalls: []tools.ToolCall{{
				Name:      tools.BoardCreateEpic,
				Arguments: `{"task_id":"CHAT-01","title":"Канбан на рефакторинг","description":"рефакторинг сервера","assigned_role":"Backend Lead"}`,
			}, {
				Name:      tools.BoardCreateTask,
				Arguments: `{"epic_id":"CHAT-01","task_id":"T-01","title":"Разбить сервер","description":"декомпозировать","assigned_role":"Senior Go Developer"}`,
			}},
			FinishReason: "tool_calls",
		},
		&runner.ModelReply{Content: "Канбан заведён.", FinishReason: "stop"},
	)

	waitBoard(t, sess, 3*time.Second, func() bool {
		epics, err := sess.board.ListEpics(context.Background())
		if err != nil {
			return false
		}
		if len(epics) != 1 {
			return false
		}
		tasks, err := sess.board.ListTasks(context.Background())
		return err == nil && len(tasks) == 1 && tasks[0].EpicID == "CHAT-01"
	})

	runScriptedChatAssistant(t, sess, "заведи баг про тормоза интерфейса",
		&runner.ModelReply{
			ToolCalls: []tools.ToolCall{{
				Name:      tools.BoardCreateBug,
				Arguments: `{"bug_id":"BUG-01","title":"Тормоза интерфейса","description":"UI фризит при открытии","epic_id":"CHAT-01"}`,
			}},
			FinishReason: "tool_calls",
		},
		&runner.ModelReply{Content: "Баг заведён.", FinishReason: "stop"},
	)

	waitBoard(t, sess, 3*time.Second, func() bool {
		bugs, err := sess.board.ListBugReports(context.Background())
		return err == nil && len(bugs) == 1 && bugs[0].BugID == "BUG-01"
	})
}

// TestChatAssistantLeavesBoardUntouched — «привет»/«как дела»: модель отвечает
// текстом без вызовов инструментов, доска не трогается.
func TestChatAssistantLeavesBoardUntouched(t *testing.T) {
	srv, _, _ := newTestServer(t)
	registerTestDir(t, srv, "proj-act-chat")
	sess, _, err := srv.getOrCreate("proj-act-chat")
	if err != nil {
		t.Fatal(err)
	}

	m := runScriptedChatAssistant(t, sess, "привет, как дела?",
		&runner.ModelReply{Content: "Привет! Всё спокойно.", FinishReason: "stop"},
	)
	if !strings.Contains(m.Content, "Привет") {
		t.Fatalf("ответ ассистента = %q", m.Content)
	}

	waitBoard(t, sess, 500*time.Millisecond, func() bool {
		epics, err := sess.board.ListEpics(context.Background())
		if err != nil {
			return false
		}
		tasks, err := sess.board.ListTasks(context.Background())
		if err != nil {
			return false
		}
		bugs, err := sess.board.ListBugReports(context.Background())
		return err == nil && len(epics) == 0 && len(tasks) == 0 && len(bugs) == 0
	})
}
