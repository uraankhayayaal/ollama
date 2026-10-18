package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai/infrastructure/sandbox"
)

func TestEnvLines(t *testing.T) {
	caps := sandbox.Capabilities{
		Docker:   sandbox.StateAvailable,
		Postgres: sandbox.StateAvailable,
		MinIO:    sandbox.StateUnknown,
	}
	lines := strings.Join(envLines(caps, "compose.yaml"), "\n")
	for _, want := range []string{
		"export SANDBOX_HAS_DOCKER=1",
		"export SANDBOX_HAS_POSTGRES=1",
		"export SANDBOX_HAS_MINIO=0",
		"export SANDBOX_COMPOSE_FILE=",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("envLines не содержит %q:\n%s", want, lines)
		}
	}
}

func TestResolveComposeEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("SANDBOX_PROVISION_COMPOSE", path)
	got, err := resolveCompose()
	if err != nil {
		t.Fatalf("resolveCompose: %v", err)
	}
	if got != path {
		t.Fatalf("got %q, ожидался %q", got, path)
	}
}

func TestResolveComposeEnvOverrideMissing(t *testing.T) {
	t.Setenv("SANDBOX_PROVISION_COMPOSE", filepath.Join(t.TempDir(), "nope.yaml"))
	if _, err := resolveCompose(); err == nil {
		t.Fatal("ожидалась ошибка для несуществующего SANDBOX_PROVISION_COMPOSE")
	}
}

func TestProvisionTimeout(t *testing.T) {
	t.Setenv("SANDBOX_PROVISION_TIMEOUT", "7")
	if got := provisionTimeout().Seconds(); got != 7 {
		t.Fatalf("таймаут %v, ожидалось 7с", got)
	}
	t.Setenv("SANDBOX_PROVISION_TIMEOUT", "bad")
	if got := provisionTimeout().Seconds(); got != 90 {
		t.Fatalf("некорректный таймаут должен давать дефолт 90с, got %v", got)
	}
}

func TestBoolBit(t *testing.T) {
	if boolBit(true) != 1 || boolBit(false) != 0 {
		t.Fatalf("boolBit: %d %d", boolBit(true), boolBit(false))
	}
}
