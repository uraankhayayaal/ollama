package sandbox

import (
	"os"
	"strings"
)

// Capabilities — возможности окружения песочницы, релевантные приёмке
// (инцидент A7-04: docs/plans/PLAN-2026-10-18-A7-04-system-fix.md и
// docs/40-operations/sandbox-readiness.md). Значения задаёт DevOps
// переменными SANDBOX_HAS_*; отсутствие переменной означает «возможности
// нет» (fail-safe: неизвестное окружение не считается полным).
//
// Declared отличает «DevOps объявил окружение» от «переменные не заданы
// вовсе»: во втором случае нельзя утверждать, что интеграционные проверки
// недоступны, — агент определяет это по факту запуска. Иначе на чистом
// dev/CI окружении промпт ошибочно запрещал бы интеграционные проверки.
type Capabilities struct {
	Docker      bool
	Postgres    bool
	MinIO       bool
	ComposeFile string
	Declared    bool
}

// ReadCapabilities читает SANDBOX_HAS_DOCKER/POSTGRES/MINIO и
// SANDBOX_COMPOSE_FILE из окружения процесса.
func ReadCapabilities() Capabilities {
	return capabilitiesFrom(os.Getenv)
}

func capabilitiesFrom(getenv func(string) string) Capabilities {
	docker := strings.TrimSpace(getenv("SANDBOX_HAS_DOCKER"))
	postgres := strings.TrimSpace(getenv("SANDBOX_HAS_POSTGRES"))
	minio := strings.TrimSpace(getenv("SANDBOX_HAS_MINIO"))
	return Capabilities{
		Docker:      envOn(docker),
		Postgres:    envOn(postgres),
		MinIO:       envOn(minio),
		ComposeFile: strings.TrimSpace(getenv("SANDBOX_COMPOSE_FILE")),
		Declared:    docker != "" || postgres != "" || minio != "",
	}
}

// Missing возвращает недоступные возможности в порядке Docker, Postgres,
// MinIO. Пустой список — окружение полное (при Declared).
func (c Capabilities) Missing() []string {
	var missing []string
	if !c.Docker {
		missing = append(missing, "Docker")
	}
	if !c.Postgres {
		missing = append(missing, "Postgres")
	}
	if !c.MinIO {
		missing = append(missing, "MinIO")
	}
	return missing
}

// Complete — доступны ли все возможности (нет пропусков).
func (c Capabilities) Complete() bool { return len(c.Missing()) == 0 }

// Summary — человекочитаемая сводка для промпта агента: что доступно и что
// считать degrade окружения, а не дефектом кода.
func (c Capabilities) Summary() string {
	if !c.Declared {
		return "возможности окружения не объявлены (SANDBOX_HAS_DOCKER/POSTGRES/MINIO) — " +
			"определи доступность интеграционных проверок по факту запуска: если проверка падает " +
			"с «connection refused» или «docker: not found», это degrade окружения (INFRA_DEGRADED), " +
			"а не дефект кода"
	}
	if c.Complete() {
		return "доступны Docker, Postgres, MinIO — интеграционные проверки выполнимы"
	}
	return "недоступно: " + strings.Join(c.Missing(), ", ") +
		" — соответствующие интеграционные проверки (E2E, migrate, ReadAppLogs) невозможны в этом " +
		"окружении: это degrade окружения (INFRA_DEGRADED), а не дефект кода"
}

// envOn трактует «1/true/yes/on» (без учёта регистра) как включено; всё
// остальное, включая пустую строку, — выключено.
func envOn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
