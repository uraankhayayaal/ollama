package main

import (
	"errors"
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

// TestInfraUnavailable — ошибки инфраструктуры отличаются от ошибок SQL:
// деграйд окружения (не дефект кода) и не должен маскироваться под сбой.
func TestInfraUnavailable(t *testing.T) {
	infra := []string{
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock",
		"docker: not found",
		"exec: \"docker\": executable file not found in $PATH",
		"service \"postgres\" is not running",
		"dial tcp 127.0.0.1:5432: connect: connection refused",
		"psql: error: password authentication failed for user \"postgres\"",
	}
	for _, out := range infra {
		if !infraUnavailable(out) {
			t.Fatalf("ожидался INFRA_DEGRADED для %q", out)
		}
	}
	code := []string{
		"psql: error: syntax error at or near \"CREAT\"",
		"ERROR:  relation \"todos\" already exists",
	}
	for _, out := range code {
		if infraUnavailable(out) {
			t.Fatalf("ошибка SQL не должна считаться инфраструктурной: %q", out)
		}
	}
}

func TestResolveSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.sql")
	if err := os.WriteFile(path, []byte("CREATE TABLE t(id int);\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("SANDBOX_SCHEMA", path)
	got, err := resolveSchema()
	if err != nil {
		t.Fatalf("resolveSchema: %v", err)
	}
	if got != path {
		t.Fatalf("got %q, ожидался %q", got, path)
	}
}

func TestResolveSchemaMissing(t *testing.T) {
	t.Setenv("SANDBOX_SCHEMA", filepath.Join(t.TempDir(), "nope.sql"))
	if _, err := resolveSchema(); err == nil {
		t.Fatal("ожидалась ошибка для несуществующего SANDBOX_SCHEMA")
	}
}

func TestPgCredentials(t *testing.T) {
	t.Setenv("SANDBOX_PG_USER", "")
	t.Setenv("SANDBOX_PG_DB", "")
	if pgUser() != "postgres" || pgDB() != "postgres" {
		t.Fatalf("дефолтные учётные данные: %q/%q", pgUser(), pgDB())
	}
	t.Setenv("SANDBOX_PG_USER", "sandbox")
	t.Setenv("SANDBOX_PG_DB", "app")
	if pgUser() != "sandbox" || pgDB() != "app" {
		t.Fatalf("учётные данные из окружения: %q/%q", pgUser(), pgDB())
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("\n\n  первая строка  \nвторая\n"); got != "первая строка" {
		t.Fatalf("firstLine = %q", got)
	}
	if got := firstLine("   \n"); got != "" {
		t.Fatalf("firstLine пустого вывода = %q", got)
	}
}

// TestMigrateWithoutStack — миграции без поднятого стека — INFRA_DEGRADED,
// а не ошибка выполнения: дефекта кода тут нет.
func TestMigrateWithoutStack(t *testing.T) {
	dir := t.TempDir()
	schema := filepath.Join(dir, "schema.sql")
	if err := os.WriteFile(schema, []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("SANDBOX_SCHEMA", schema)
	compose := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(compose, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := migrateSchema(compose)
	if err == nil {
		t.Skip("в окружении доступен демон Docker и сервис postgres — деграйд не воспроизводится")
	}
	var infra infraError
	if !errors.As(err, &infra) {
		t.Fatalf("ожидалась infraError, получено %T: %v", err, err)
	}
}
