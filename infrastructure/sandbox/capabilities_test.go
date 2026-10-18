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
	want := Capabilities{
		Docker:      StateAvailable,
		Postgres:    StateAvailable,
		MinIO:       StateAvailable,
		ComposeFile: "compose.yaml",
	}
	if got != want {
		t.Fatalf("capabilitiesFrom = %+v, want %+v", got, want)
	}
}

func TestStateFromEnvUnknownVsDeclared(t *testing.T) {
	if got := stateFromEnv(""); got != StateUnknown {
		t.Fatalf("пустая переменная = %v, ожидалось StateUnknown", got)
	}
	if got := stateFromEnv("0"); got != StateUnavailable {
		t.Fatalf("явный 0 = %v, ожидалось StateUnavailable", got)
	}
	if got := stateFromEnv("1"); got != StateAvailable {
		t.Fatalf("1 = %v, ожидалось StateAvailable", got)
	}
}

func TestResolveCapabilitiesEnvWinsDetectionFillsUnknown(t *testing.T) {
	env := func(k string) string { return map[string]string{"SANDBOX_HAS_DOCKER": "0"}[k] }
	detect := func() Capabilities {
		return Capabilities{Docker: StateAvailable, Postgres: StateAvailable, MinIO: StateUnknown}
	}
	got := resolveCapabilities(env, detect)
	// Явное 0 от DevOps важнее пробы.
	if got.Docker != StateUnavailable {
		t.Errorf("Docker = %v, явное объявление 0 должно победить пробу", got.Docker)
	}
	// Неизвестные уточняются пробой.
	if got.Postgres != StateAvailable {
		t.Errorf("Postgres = %v, ожидалось StateAvailable из пробы", got.Postgres)
	}
	if got.MinIO != StateUnknown {
		t.Errorf("MinIO = %v, проба не подтвердила — должно остаться StateUnknown", got.MinIO)
	}
}

func TestSummaryStates(t *testing.T) {
	allUnknown := Capabilities{}
	if s := allUnknown.Summary(); !strings.Contains(s, "не объявлены") || strings.Contains(s, "невозможны") {
		t.Fatalf("пустое окружение Summary=%q: ожидалось «не объявлены» без «невозможны»", s)
	}

	unavail := Capabilities{Docker: StateUnavailable, Postgres: StateUnknown, MinIO: StateUnknown}
	if s := unavail.Summary(); !strings.Contains(s, "INFRA_DEGRADED") || !strings.Contains(s, "Docker") {
		t.Fatalf("явно недоступное Summary=%q, ожидалось упоминание INFRA_DEGRADED/Docker", s)
	}

	full := Capabilities{Docker: StateAvailable, Postgres: StateAvailable, MinIO: StateAvailable}
	if s := full.Summary(); !strings.Contains(s, "выполнимы") {
		t.Fatalf("полное окружение Summary=%q, ожидалось «выполнимы»", s)
	}

	partial := Capabilities{Docker: StateAvailable, Postgres: StateUnknown, MinIO: StateUnknown}
	s := partial.Summary()
	if !strings.Contains(s, "доступно: Docker") || !strings.Contains(s, "не объявлено") || !strings.Contains(s, "по факту") {
		t.Fatalf("частичное окружение Summary=%q, ожидалось «доступно: Docker» + «не объявлено … по факту»", s)
	}
}

func TestCapabilitiesAvailableUnavailable(t *testing.T) {
	c := Capabilities{Docker: StateAvailable, Postgres: StateUnavailable, MinIO: StateUnknown}
	if got := c.Available(); len(got) != 1 || got[0] != "Docker" {
		t.Fatalf("Available = %v, want [Docker]", got)
	}
	if got := c.Unavailable(); len(got) != 1 || got[0] != "Postgres" {
		t.Fatalf("Unavailable = %v, want [Postgres]", got)
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
