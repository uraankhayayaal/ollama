// Package sandbox — изоляция выполнения команд агентов (Этап 1
// enterprise-плана: изоляция рабочих пространств).
//
// Пакет владеет абстракцией Workspace: любое исполнение команды агента идёт
// через неё, а не через прямой exec на хосте. Две реализации:
//
//   - LocalWorkspace — прежнее поведение (sh -c на хосте, убийство группы
//     процессов по таймауту). Ноль-конфигурации, работает всегда;
//   - DockerWorkspace — контейнер на сессию оркестрации: команда выполняется
//     через Docker Engine API (POST /containers/{id}/exec) внутри долгоживущего
//     контейнера с рабочими каталогами, смонтированными по тем же путям, что на
//     хосте. Изоляция: non-root, cap-drop ALL, лимиты CPU/RAM, сеть по
//     умолчанию отключена (CODEGEN_SANDBOX=session), белый список доменов через
//     egress-прокси (CODEGEN_SANDBOX_ALLOW_DOMAINS).
//
// Сознательные границы пакета:
//
//   - ФАЙЛОВЫЕ операции (чтение/запись) остаются на хосте: рабочие каталоги
//     монтируются bind-mount'ом по исходным путям, поэтому хост и контейнер
//     видят одни и те же файлы. Полное прятанье файлового слоя за интерфейсом
//     не нужно — оно бы только добавило сетевых кругов на каждую операцию;
//   - ephemeral-контейнер на команду (CODEGEN_SANDBOX=container, docker run
//     через CLI) живёт в пакете tools и намеренно не тронут: он покрывает
//     разовые запуски вне оркестрации;
//   - отбраковка разрушительных команд (DestructiveReason) живёт здесь, чтобы
//     и локальный, и контейнерный путь проверяли её одинаково.
package sandbox

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Mode — выбранный исполнитель команд.
type Mode string

const (
	// ModeLocal — хост (поведение по умолчанию, без изоляции).
	ModeLocal Mode = "local"
	// ModeContainer — эфемерный docker run на каждую команду (Ф-4, CLI).
	ModeContainer Mode = "container"
	// ModeSession — контейнер на сессию оркестрации (Этап 1, п. 1.5):
	// сервер поднимает DockerWorkspace перед runner.Run и снимает после.
	ModeSession Mode = "session"
	// ModeUnset — режим не определён (сбой разбора окружения).
	ModeUnset Mode = ""
)

// Config — разобранная конфигурация песочницы. Ноль-значения безопасны:
// конфигурация без флагов даёт рабочее окружение (запись в проект разрешена,
// кэши в tmpfs), как и прежде.
type Config struct {
	Mode  Mode
	Image string
	// Network — сеть контейнера: default / none / bridge / whitelist.
	// whitelist — внутренняя сеть Docker без маршрута наружу + HTTP(S)-прокси
	// с белым списком доменов (п. 1.11); для ModeSession это значение по
	// умолчанию при заданном AllowDomains.
	Network  string
	Memory   string
	CPUs     string
	ReadOnly bool // read-only корень контейнера (CODEGEN_SANDBOX_RO)
	// WorkdirReadOnly — рабочий каталог монтируется только на чтение
	// (CODEGEN_SANDBOX_ALLOW_WRITE=false).
	WorkdirReadOnly bool
	// AllowDomains — белый список доменов исходящего трафика (1.11):
	// "example.com", "*.example.com". Пусто — при сетевой изоляции (none/
	// whitelist без доменов) исходящего трафика нет вовсе.
	AllowDomains []string
	// LocalCommand — исполнитель на хосте для LocalWorkspace. Продакшн nil
	// (тогда exec.Command), в тестах подменяется заглушкой: hermetic-тесты
	// не должны дёргать shell.
	LocalCommand func(command, workdir string) (*exec.Cmd, error)
}

// LoadConfig читает CODEGEN_SANDBOX* и определяет исполнитель.
//
// По умолчанию — ХОСТ, и это осознанно: молчаливый перенос ВСЕГО исполнения
// в контейнер — поведенческое изменение, ломающее проекты по неочевидной
// причине. Песочница включается явно (CODEGEN_SANDBOX=container/session/auto).
//
// Для ModeSession поведение сети отличается (п. 1.10): если
// CODEGEN_SANDBOX_NETWORK не задан явно, сессия получает изоляцию — "none",
// либо "whitelist" при заданном CODEGEN_SANDBOX_ALLOW_DOMAINS. Нет ни явного
// флага — нет интернета по умолчанию.
func LoadConfig() Config {
	cfg := Config{Mode: ModeLocal}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX"))) {
	case "1", "on", "true", "yes", "container", "docker":
		cfg.Mode = ModeContainer
	case "session", "containersession", "container-session":
		cfg.Mode = ModeSession
	case "auto":
		if dockerDaemonAvailable() {
			cfg.Mode = ModeContainer
		} else {
			cfg.Mode = ModeLocal
		}
	case "", "0", "off", "false", "no", "local", "host":
		cfg.Mode = ModeLocal
	default:
		// Неизвестное значение — не угадываем: хост с честной пометкой.
		cfg.Mode = ModeLocal
	}
	cfg.Image = FirstEnv("CODEGEN_SANDBOX_IMAGE", "CODEGEN_IMAGE")

	rawNet := FirstEnv("CODEGEN_SANDBOX_NETWORK", "CODEGEN_SANDBOX_NET")
	switch rawNet {
	case "", "default", "host":
		cfg.Network = "default"
	case "none":
		cfg.Network = "none"
	case "bridge":
		cfg.Network = "bridge"
	case "whitelist":
		cfg.Network = "whitelist"
	default:
		cfg.Network = rawNet
	}
	// Изоляция по умолчанию для сессии (1.10): явный флаг сети отменяет.
	if cfg.Mode == ModeSession && rawNet == "" {
		cfg.AllowDomains = parseDomains(os.Getenv("CODEGEN_SANDBOX_ALLOW_DOMAINS"))
		if len(cfg.AllowDomains) > 0 {
			cfg.Network = "whitelist"
		} else {
			cfg.Network = "none"
		}
	}
	// whitelist вне сессии (эфемерный контейнер) не поддержан: прокси живёт
	// вместе с долгоживущим контейнером. Честнее откатиться на открытую сеть,
	// чем сломать docker run невалидным --network.
	if cfg.Network == "whitelist" && cfg.Mode != ModeSession {
		cfg.Network = "default"
	}
	if cfg.Mode == ModeSession && cfg.Network == "whitelist" && len(cfg.AllowDomains) == 0 {
		cfg.Network = "none"
	}

	cfg.Memory = firstEnvOr("2g", "CODEGEN_SANDBOX_MEMORY", "CODEGEN_MEMORY")
	cfg.CPUs = firstEnvOr("2", "CODEGEN_SANDBOX_CPUS", "CODEGEN_CPUS")
	if v := strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX_RO")); v != "" {
		if ro, err := strconv.ParseBool(v); err == nil {
			cfg.ReadOnly = ro
		}
	}
	if v := strings.TrimSpace(os.Getenv("CODEGEN_SANDBOX_ALLOW_WRITE")); v != "" {
		if w, err := strconv.ParseBool(v); err == nil {
			cfg.WorkdirReadOnly = !w
		}
	}
	// Домены читаются и здесь (а не только в ветке session): тесты и сервер
	// зовут LoadConfig в разных режимах, а поле должно быть согласованным.
	if len(cfg.AllowDomains) == 0 {
		cfg.AllowDomains = parseDomains(os.Getenv("CODEGEN_SANDBOX_ALLOW_DOMAINS"))
	}
	return cfg
}

// parseDomains разбирает белый список доменов: запятая, точка с запятой или
// пробел — разделители; регистр и пустые элементы отбрасываются.
func parseDomains(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Result — результат выполнения одной команды в Workspace.
type Result struct {
	Stdout string
	Stderr string
	// ExitCode — код выхода; -1, если неизвестен (не стартовал / убит по
	// таймауту до получения кода).
	ExitCode int
	// TimedOut — команда остановлена по таймауту (не путать с ненулевым
	// выходом: это разные состояния для модели).
	TimedOut bool
	// ExitErr — ошибка завершения (ненулевой выход). nil при успехе.
	// Ошибки ЗАПУСКА (docker недоступен, каталог не найден) возвращаются
	// отдельной ошибкой Run — вызывающий различает «упала команда» и
	// «не удалось выполнить».
	ExitErr error
}

// Workspace — изолированное окружение выполнения команд.
//
// Контракт: Run не должен зависать дольше дедлайна ctx; по дедлайну команда
// останавливается, а результат помечается TimedOut. Run возвращает ошибку
// только за инфраструктурные сбои (нет демона, каталог вне монтирования);
// ненулевой выход команды — это Result, а не ошибка.
type Workspace interface {
	Run(ctx context.Context, workdir, command string) (Result, error)
	Close(ctx context.Context) error
}

// FirstEnv — первый непустой env из списка имён.
func FirstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// firstEnvOr — первый непустой env из списка имён, иначе def.
func firstEnvOr(def string, names ...string) string {
	if v := FirstEnv(names...); v != "" {
		return v
	}
	return def
}

// Tmp — каталог для HOME и всех кэшей внутри контейнера. Именно tmpfs: он
// существует в ЛЮБОМ образе, не переживает конец запуска и не оставляет
// в проекте мусор вида ~/.npmrc, который попал бы в git diff.
const Tmp = "/tmp"

// BaseEnv — окружение внутри контейнера песочницы. Совпадает с
// sandbox/Dockerfile и sandbox/compose.yaml: HOME и все кэши в /tmp, чтобы
// рабочий каталог был единственным местом записи.
func BaseEnv() map[string]string {
	return map[string]string{
		"HOME":                     Tmp,
		"XDG_CACHE_HOME":           Tmp + "/.cache",
		"GOCACHE":                  Tmp + "/.cache/go-build",
		"GOMODCACHE":               Tmp + "/go/pkg/mod",
		"GOPATH":                   Tmp + "/go",
		"npm_config_cache":         Tmp + "/.npm",
		"PIP_CACHE_DIR":            Tmp + "/.cache/pip",
		"PLAYWRIGHT_BROWSERS_PATH": Tmp + "/ms-playwright",
		"CI":                       "true",
		// Коммит из песочницы (через make-цели приёмки) не должен падать из-за
		// отсутствия ~/.gitconfig: HOME — tmpfs, глобального конфига в ней нет,
		// а без него git не может определить автора коммита.
		"GIT_CONFIG_GLOBAL":   Tmp + "/.gitconfig",
		"GIT_AUTHOR_NAME":     "AI Sandbox",
		"GIT_AUTHOR_EMAIL":    "sandbox@localhost",
		"GIT_COMMITTER_NAME":  "AI Sandbox",
		"GIT_COMMITTER_EMAIL": "sandbox@localhost",
	}
}

// HostUser — идентификаторы текущего пользователя хоста в формате uid:gid
// для --user контейнера. Берутся из os.Getuid/Getgid: файлы, которые агент
// пишет в смонтированный каталог, должны оставаться его владельца на хосте.
// На Windows идентификаторов нет — пустая строка.
func HostUser() string {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return ""
	}
	return strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
}

// exitStatusError — ошибка завершения с кодом. Текст совпадает с
// (*exec.ExitError).Error() ("exit status N"), чтобы вызывающий код
// (missingToolHint и др.) различал причины одинаково на хосте и в контейнере.
type exitStatusError int

func (e exitStatusError) Error() string { return "exit status " + strconv.Itoa(int(e)) }

// ExitCode — код выхода для универсальных проверок вызывающего (например,
// missingToolHint ищет 127 и опирается на interface{ ExitCode() int }).
func (e exitStatusError) ExitCode() int { return int(e) }
