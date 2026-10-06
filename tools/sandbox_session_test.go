package tools

// sandbox_session_test.go — маршрутизация команды в сессионную песочницу
// (Этап 1): реестр активных контейнеров важнее локального режима, а
// CODEGEN_SANDBOX=session без контейнера — строгая ошибка, а не тихий хост.

import (
	"context"
	"strings"
	"testing"

	"ai/sandbox"
)

// stubWS — поддельный сессионный Workspace.
type stubWS struct {
	ran     bool
	gotDir  string
	out     string
	network string
	err     error
}

func (s *stubWS) Run(_ context.Context, workdir, _ string) (sandbox.Result, error) {
	s.ran = true
	s.gotDir = workdir
	if s.err != nil {
		return sandbox.Result{}, s.err
	}
	return sandbox.Result{Stdout: s.out, ExitCode: 0}, nil
}
func (s *stubWS) Close(context.Context) error { return nil }

// SandboxNetwork — как у настоящего DockerWorkspace (подсказка о сети).
func (s *stubWS) SandboxNetwork() string {
	if s.network == "" {
		return "bridge"
	}
	return s.network
}

// Активная сессия перекрывает локальный режим: сервер поднял контейнер —
// команда обязана уйти в него, даже если окружение говорит «local».
func TestRunCommandPrefersActiveSession(t *testing.T) {
	dir := t.TempDir()
	stub := &stubWS{out: "из контейнера"}
	sandbox.Activate("sess-pref", stub, dir, []string{dir})
	defer sandbox.Deactivate("sess-pref")
	t.Setenv("CODEGEN_SANDBOX", "local")

	res, err := runCommand("echo hi", dir, "sess-pref")
	if err != nil {
		t.Fatal(err)
	}
	if !stub.ran || stub.gotDir != dir {
		t.Errorf("команда не дошла до сессии: ran=%v dir=%q", stub.ran, stub.gotDir)
	}
	if res["sandbox"] != string(SandboxModeSession) {
		t.Errorf("sandbox = %q, ожидался session", res["sandbox"])
	}
	if res["status"] != "success" || res["stdout"] != "из контейнера" {
		t.Errorf("неожиданный результат: %+v", res)
	}
}

// Подсказка о работе без интернета приходит из режима сети сессии.
func TestRunCommandSessionNetworkHint(t *testing.T) {
	dir := t.TempDir()
	stub := &stubWS{out: "ok", network: "none"}
	sandbox.Activate("sess-net", stub, dir, []string{dir})
	defer sandbox.Deactivate("sess-net")

	res, err := runCommand("echo hi", dir, "sess-net")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res["hint"], "БЕЗ СЕТИ") {
		t.Errorf("при сети none ожидалась подсказка про офлайн: %+v", res)
	}
}

// CODEGEN_SANDBOX=session, но контейнера нет (сервер не поднял сессию) —
// строгая ошибка в результате. Тихий запуск на хосте запрещён (п. 1.3).
func TestRunCommandSessionWithoutContainerIsStrictError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEGEN_SANDBOX", "session")

	res, err := runCommand("echo hi", dir, "no-such-session")
	if err != nil {
		t.Fatalf("session без контейнера должен давать результат-ошибку, не падение: %v", err)
	}
	if res["status"] != "error" || res["sandbox"] != string(SandboxModeSession) {
		t.Errorf("ожидался status=error, sandbox=session: %+v", res)
	}
	if res["stdout"] != "" || res["stderr"] != "" {
		t.Errorf("команда не должна была выполниться: %+v", res)
	}
	if !strings.Contains(res["message"], "не активен") {
		t.Errorf("сообщение должно объяснять отсутствие сессии: %q", res["message"])
	}
}

// Разрушительная команда блокируется и на сессионном пути — до вызова
// Workspace.Run (общий гейт).
func TestRunCommandSessionBlocksDestructive(t *testing.T) {
	dir := t.TempDir()
	stub := &stubWS{}
	sandbox.Activate("sess-destr", stub, dir, []string{dir})
	defer sandbox.Deactivate("sess-destr")

	res, err := runCommand("rm -rf /", dir, "sess-destr")
	if err != nil {
		t.Fatal(err)
	}
	if stub.ran {
		t.Error("разрушительная команда не должна была дойти до сессии")
	}
	if res["status"] != "error" || !strings.Contains(res["exit_error"], "безопасности") {
		t.Errorf("ожидался отказ политики: %+v", res)
	}
}
