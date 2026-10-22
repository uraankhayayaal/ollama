package sandbox

import (
	"context"
	"net"
	"os"
	"strings"
	"time"
)

// State — состояние отдельной возможности окружения. Разделяет «неизвестно»
// и «явно недоступно»: только явную недоступность (объявил DevOps или
// доказала проба) можно трактовать как degrade; «неизвестно» агент
// определяет по факту запуска.
type State int

const (
	// StateUnknown — не объявлено и не обнаружено.
	StateUnknown State = iota
	// StateAvailable — объявлено DevOps или обнаружено пробой.
	StateAvailable
	// StateUnavailable — объявлено DevOps как недоступное.
	StateUnavailable
)

func (s State) String() string {
	switch s {
	case StateAvailable:
		return "доступно"
	case StateUnavailable:
		return "недоступно"
	default:
		return "неизвестно"
	}
}

// Capabilities — возможности окружения песочницы, релевантные приёмке
// (инцидент A7-04: docs/plans/PLAN-2026-10-18-A7-04-system-fix.md и
// docs/40-operations/sandbox-readiness.md).
type Capabilities struct {
	Docker      State
	Postgres    State
	MinIO       State
	ComposeFile string
}

// ReadCapabilities читает только объявленные DevOps переменные
// SANDBOX_HAS_DOCKER/POSTGRES/MINIO и SANDBOX_COMPOSE_FILE. Отсутствие
// переменной — StateUnknown (не «недоступно»).
func ReadCapabilities() Capabilities {
	return capabilitiesFrom(os.Getenv)
}

func capabilitiesFrom(getenv func(string) string) Capabilities {
	return Capabilities{
		Docker:      stateFromEnv(getenv("SANDBOX_HAS_DOCKER")),
		Postgres:    stateFromEnv(getenv("SANDBOX_HAS_POSTGRES")),
		MinIO:       stateFromEnv(getenv("SANDBOX_HAS_MINIO")),
		ComposeFile: strings.TrimSpace(getenv("SANDBOX_COMPOSE_FILE")),
	}
}

// DetectCapabilities активно проверяет окружение: Docker — ping демона,
// Postgres/MinIO — TCP-доступность типовых портов. Проба может только
// подтвердить доступность (StateAvailable); если проверка не прошла,
// возможность остаётся StateUnknown — слабый сигнал не должен превращаться
// в «недоступно» и запрещать интеграционные проверки.
func DetectCapabilities() Capabilities {
	return Capabilities{
		Docker:   detectState(dockerPing()),
		Postgres: detectState(dialPort(postgresAddr())),
		MinIO:    detectState(dialPort(minioAddr())),
	}
}

// Порты песочничного стека на хосте по умолчанию. Переопределяются
// переменными SANDBOX_PG_HOST_PORT / SANDBOX_S3_HOST_PORT: на машине, где
// 5432/9000 уже заняты другим стеком, песочница поднимается на свободных
// портах — иначе подъём падал бы «port is already allocated», что выглядело
// бы как дефект кода.
const (
	defaultPostgresPort = "5432"
	defaultMinioPort    = "9000"
)

// postgresAddr — адрес Postgres песочницы на хосте.
func postgresAddr() string {
	return "127.0.0.1:" + portEnv("SANDBOX_PG_HOST_PORT", defaultPostgresPort)
}

// minioAddr — адрес S3-хранилища песочницы на хосте.
func minioAddr() string {
	return "127.0.0.1:" + portEnv("SANDBOX_S3_HOST_PORT", defaultMinioPort)
}

// portEnv — порт из окружения или значение по умолчанию.
func portEnv(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

// ResolveCapabilities — итоговые возможности для промпта: приоритет у явного
// объявления DevOps, недостающие («неизвестные») возможности уточняются
// пробой.
func ResolveCapabilities() Capabilities {
	return resolveCapabilities(os.Getenv, DetectCapabilities)
}

func resolveCapabilities(getenv func(string) string, detect func() Capabilities) Capabilities {
	c := capabilitiesFrom(getenv)
	if c.Docker != StateUnknown && c.Postgres != StateUnknown && c.MinIO != StateUnknown {
		return c
	}
	d := detect()
	if c.Docker == StateUnknown {
		c.Docker = d.Docker
	}
	if c.Postgres == StateUnknown {
		c.Postgres = d.Postgres
	}
	if c.MinIO == StateUnknown {
		c.MinIO = d.MinIO
	}
	return c
}

func detectState(ok bool) State {
	if ok {
		return StateAvailable
	}
	return StateUnknown
}

func stateFromEnv(v string) State {
	if strings.TrimSpace(v) == "" {
		return StateUnknown
	}
	if envOn(v) {
		return StateAvailable
	}
	return StateUnavailable
}

// Unavailable возвращает имена явно недоступных возможностей (degrade).
func (c Capabilities) Unavailable() []string {
	return c.names(func(s State) bool { return s == StateUnavailable })
}

// Available возвращает имена доступных возможностей.
func (c Capabilities) Available() []string {
	return c.names(func(s State) bool { return s == StateAvailable })
}

func (c Capabilities) names(want func(State) bool) []string {
	var out []string
	add := func(name string, s State) {
		if want(s) {
			out = append(out, name)
		}
	}
	add("Docker", c.Docker)
	add("Postgres", c.Postgres)
	add("MinIO", c.MinIO)
	return out
}

// Summary — человекочитаемая сводка для промпта агента: что доступно и что
// считать degrade окружения, а не дефектом кода.
func (c Capabilities) Summary() string {
	unavail := c.Unavailable()
	avail := c.Available()
	var unknown []string
	for _, name := range []string{"Docker", "Postgres", "MinIO"} {
		if !contains(unavail, name) && !contains(avail, name) {
			unknown = append(unknown, name)
		}
	}

	switch {
	case len(unavail) > 0:
		return "недоступно: " + strings.Join(unavail, ", ") +
			" — соответствующие интеграционные проверки (E2E, migrate, ReadAppLogs) невозможны в этом " +
			"окружении: это degrade окружения (INFRA_DEGRADED), а не дефект кода"
	case len(unknown) == 0:
		return "доступны Docker, Postgres, MinIO — интеграционные проверки выполнимы"
	case len(avail) > 0:
		return "доступно: " + strings.Join(avail, ", ") + "; не объявлено: " + strings.Join(unknown, ", ") +
			" — определи доступность по факту запуска (при «connection refused»/«docker: not found» это " +
			"INFRA_DEGRADED, а не дефект кода)"
	default:
		return "возможности окружения не объявлены (SANDBOX_HAS_DOCKER/POSTGRES/MINIO) — " +
			"определи доступность интеграционных проверок по факту запуска: если проверка падает " +
			"с «connection refused» или «docker: not found», это degrade окружения (INFRA_DEGRADED), " +
			"а не дефект кода"
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// dockerPing — короткая проверка доступности демона Docker (не 2 с, как в
// EngineAvailable: проба вызывается при построении промпта).
func dockerPing() bool {
	eng, err := NewEngineFromEnv()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	return eng.Ping(ctx) == nil
}

// dialPort — TCP-доступность адреса за короткий таймаут.
func dialPort(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// envOn трактует «1/true/yes/on» (без учёта регистра) как включено.
func envOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
