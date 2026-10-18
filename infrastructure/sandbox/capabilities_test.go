package sandbox

import (
	"strings"
	"testing"
)

func TestCapabilitiesFromReadsEnv(t *testing.T) {
	env := map[string]string{
		"SANDBOX_HAS_DOCKER":   "1",
		"SANDBOX_HAS_POSTGRES": "true",
		"SANDBOX_HAS_MINIO":    "YES",
		"SANDBOX_COMPOSE_FILE": " compose.yaml ",
	}
	got := capabilitiesFrom(func(k string) string { return env[k] })
	want := Capabilities{Docker: true, Postgres: true, MinIO: true, ComposeFile: "compose.yaml", Declared: true}
	if got != want {
		t.Fatalf("capabilitiesFrom = %+v, want %+v", got, want)
	}
}

func TestCapabilitiesUndeclaredIsNotDegraded(t *testing.T) {
	empty := capabilitiesFrom(func(string) string { return "" })
	if empty.Declared {
		t.Fatal("без SANDBOX_HAS_* окружение не должно считаться объявленным")
	}
	// Ключевое: на чистом dev/CI промпт не должен утверждать, что проверки
	// недоступны, — только просить определить по факту.
	s := empty.Summary()
	if !strings.Contains(s, "не объявлены") || !strings.Contains(s, "по факту") {
		t.Fatalf("Summary необъявленного окружения=%q, ожидалось «не объявлены … по факту»", s)
	}
	if strings.Contains(s, "невозможны") {
		t.Fatalf("Summary не должен утверждать невозможность без объявления: %q", s)
	}
}

func TestCapabilitiesMissingAndSummary(t *testing.T) {
	declaredEmpty := capabilitiesFrom(func(k string) string {
		if k == "SANDBOX_HAS_DOCKER" {
			return "0"
		}
		return ""
	})
	if !declaredEmpty.Declared {
		t.Fatal("явная установка SANDBOX_HAS_DOCKER=0 должна помечать окружение объявленным")
	}
	if got := declaredEmpty.Missing(); len(got) != 3 || got[0] != "Docker" || got[1] != "Postgres" || got[2] != "MinIO" {
		t.Fatalf("Missing = %v, want [Docker Postgres MinIO]", got)
	}
	if s := declaredEmpty.Summary(); !strings.Contains(s, "degrade окружения") || !strings.Contains(s, "Docker") {
		t.Fatalf("Summary деградированного окружения=%q, ожидалось упоминание degrade и Docker", s)
	}

	full := Capabilities{Docker: true, Postgres: true, MinIO: true, Declared: true}
	if !full.Complete() {
		t.Fatal("полный набор должен быть Complete")
	}
	if s := full.Summary(); !strings.Contains(s, "выполнимы") {
		t.Fatalf("Summary полного окружения=%q, ожидалось «выполнимы»", s)
	}

	partial := Capabilities{Docker: true}
	if got := partial.Missing(); len(got) != 2 || got[0] != "Postgres" || got[1] != "MinIO" {
		t.Fatalf("Missing частичного окружения = %v, want [Postgres MinIO]", got)
	}
}

func TestEnvOn(t *testing.T) {
	for _, on := range []string{"1", "true", "TRUE", "Yes", "on", " 1 "} {
		if !envOn(on) {
			t.Errorf("envOn(%q) = false, want true", on)
		}
	}
	for _, off := range []string{"", "0", "false", "no", "off", "maybe"} {
		if envOn(off) {
			t.Errorf("envOn(%q) = true, want false", off)
		}
	}
}
