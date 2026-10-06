package server

// sandbox_test.go — старт/стоп сессионной песочницы проекта (Этап 1, п. 1.5).

import (
	"context"
	"path/filepath"
	"testing"

	"ai/sandbox"
	"ai/workspace"
)

func newSandboxTestServer(t *testing.T) *Server {
	t.Helper()
	reg, err := workspace.Open(filepath.Join(t.TempDir(), "workspaces.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{reg: reg}
}

func hasProject(projects []string, name string) bool {
	for _, p := range projects {
		if p == name {
			return true
		}
	}
	return false
}

// Без CODEGEN_SANDBOX=session старт — no-op: прежнее поведение не меняется.
func TestStartProjectSandboxNoopWithoutSessionMode(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "local")
	s := newSandboxTestServer(t)
	if err := s.startProjectSandbox(context.Background(), "noop-proj"); err != nil {
		t.Fatalf("no-op обязан проходить без ошибок: %v", err)
	}
	if hasProject(sandbox.ActiveProjects(), "noop-proj") {
		t.Error("вне session в реестре не должно появляться сессий")
	}
	// Стоп без сессии — no-op.
	s.stopProjectSandbox("noop-proj")
}

// session + недоступный Docker (битая сокет-путь) — ошибка старта, а не
// тихий переход на хост; запись в реестре не остаётся.
func TestStartProjectSandboxFailsWithoutDocker(t *testing.T) {
	t.Setenv("CODEGEN_SANDBOX", "session")
	t.Setenv("CODEGEN_DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "no-such-docker.sock"))
	s := newSandboxTestServer(t)

	err := s.startProjectSandbox(context.Background(), "nodocker-proj")
	if err == nil {
		t.Fatal("session без доступного Docker обязан отказывать в старте")
	}
	if hasProject(sandbox.ActiveProjects(), "nodocker-proj") {
		t.Error("при неудачном старте сессия не должна остаться в реестре")
	}
	// Стоп после неудачи — no-op без паники.
	s.stopProjectSandbox("nodocker-proj")
}
