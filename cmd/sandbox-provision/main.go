// Command sandbox-provision — провижининг стека Postgres + MinIO для песочницы
// авто-проверки (п.3.1 A7-04, docs/40-operations/sandbox-readiness.md).
//
// Подкоманды:
//
//	up      — `docker compose up -d --wait` и ожидание доступности Postgres/MinIO
//	down    — `docker compose down` (доп. аргументы прокидываются, напр. -v)
//	status  — состояние возможностей окружения (sandbox.DetectCapabilities)
//	env     — экспорт SANDBOX_HAS_*/SANDBOX_COMPOSE_FILE для оркестратора
//	migrate — применение схемы (миграций) к Postgres песочницы
//
// Путь к compose: SANDBOX_PROVISION_COMPOSE или
// infrastructure/sandbox/provision/compose.yaml от текущего каталога.
//
// Коды возврата: 0 — ок, 2 — INFRA_DEGRADED (инфраструктура недоступна: это
// не дефект кода), 1 — ошибка выполнения.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai/infrastructure/sandbox"
)

const (
	defaultCompose = "infrastructure/sandbox/provision/compose.yaml"
	// defaultSchema — DDL песочничной БД (переопределяется SANDBOX_SCHEMA).
	defaultSchema = "server/testdata/todoapp/schema.sql"
)

// exitInfra — код возврата для INFRA_DEGRADED. Отдельный от 1 (ошибка
// выполнения), чтобы CI и оркестратор отличали деграйд инфраструктуры от
// дефекта кода: деграйд не блокирует unit/contract-проверки.
const exitInfra = 2

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	compose, err := resolveCompose()
	if err != nil {
		die(err)
	}
	switch os.Args[1] {
	case "up":
		if err := composeRun(compose, "up", "-d", "--wait"); err != nil {
			die(err)
		}
		if err := waitAvailable(provisionTimeout()); err != nil {
			die(err)
		}
		fmt.Println("OK:", sandbox.DetectCapabilities().Summary())
		fmt.Println(`Примените окружение: eval "$(go run ./cmd/sandbox-provision env)"`)
	case "down":
		if err := composeRun(compose, append([]string{"down"}, os.Args[2:]...)...); err != nil {
			die(err)
		}
	case "status":
		caps := sandbox.DetectCapabilities()
		fmt.Println(caps.Summary())
		fmt.Printf("docker=%s postgres=%s minio=%s\n", caps.Docker, caps.Postgres, caps.MinIO)
	case "env":
		for _, line := range envLines(sandbox.DetectCapabilities(), compose) {
			fmt.Println(line)
		}
	case "migrate":
		if err := migrateSchema(compose); err != nil {
			die(err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sandbox-provision <up|down|status|env|migrate>")
}

func die(err error) {
	var infra infraError
	if errors.As(err, &infra) {
		fmt.Fprintln(os.Stderr, "sandbox-provision: INFRA_DEGRADED:", infra.msg)
		os.Exit(exitInfra)
	}
	fmt.Fprintln(os.Stderr, "sandbox-provision:", err)
	os.Exit(1)
}

// infraError — ошибка инфраструктуры (демон Docker недоступен, сервис не
// поднят, БД не отвечает). Не дефект кода: вызывающий помечает окружение
// как INFRA_DEGRADED и не возвращает задачу разработчику.
type infraError struct{ msg string }

func (e infraError) Error() string { return e.msg }

func infraf(format string, args ...any) error {
	return infraError{msg: fmt.Sprintf(format, args...)}
}

// resolveCompose находит compose-файл стека: явный SANDBOX_PROVISION_COMPOSE
// приоритетнее пути по умолчанию.
func resolveCompose() (string, error) {
	if p := strings.TrimSpace(os.Getenv("SANDBOX_PROVISION_COMPOSE")); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("SANDBOX_PROVISION_COMPOSE=%s: %w", p, err)
		}
		return p, nil
	}
	if _, err := os.Stat(defaultCompose); err == nil {
		return defaultCompose, nil
	}
	return "", fmt.Errorf("compose-файл не найден (%s); задайте SANDBOX_PROVISION_COMPOSE", defaultCompose)
}

// resolveSchema находит файл схемы песочничной БД: SANDBOX_SCHEMA или путь
// по умолчанию.
func resolveSchema() (string, error) {
	p := strings.TrimSpace(os.Getenv("SANDBOX_SCHEMA"))
	if p == "" {
		p = defaultSchema
	}
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("файл схемы не найден (%s): %w", p, err)
	}
	return p, nil
}

// composeRun запускает `docker compose -f <compose> <args...>` с прозрачным
// пробросом вывода.
func composeRun(compose string, args ...string) error {
	full := append([]string{"compose", "-f", compose}, args...)
	cmd := exec.Command("docker", full...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// composeExec выполняет `docker compose -f <compose> <args...>` с заданным
// stdin и возвращает объединённый вывод. Используется для psql: DDL идёт
// на stdin, драйвер БД в модуле не нужен.
func composeExec(compose string, stdin io.Reader, args ...string) (string, error) {
	full := append([]string{"compose", "-f", compose}, args...)
	cmd := exec.Command("docker", full...)
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// migrateSchema применяет схему песочницы (миграции) к Postgres стека:
// DDL выполняется psql внутри контейнера. Ошибки инфраструктуры (нет
// демона, сервис не поднят, БД не отвечает) — INFRA_DEGRADED; ошибка SQL —
// обычный сбой (exit 1).
func migrateSchema(compose string) error {
	schemaPath, err := resolveSchema()
	if err != nil {
		return err
	}
	ddl, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("чтение схемы %s: %w", schemaPath, err)
	}
	if !dockerReady() {
		return infraf("docker-демон недоступен (docker info) — стек песочницы не поднят")
	}
	if id := composeServiceID(compose, "postgres"); id == "" {
		return infraf("контейнер postgres не создан — выполните `sandbox-provision up`")
	}
	out, err := composeExec(compose, strings.NewReader(string(ddl)),
		"exec", "-T", "postgres", "psql", "-v", "ON_ERROR_STOP=1",
		"-U", pgUser(), "-d", pgDB(), "-f", "-")
	if err != nil {
		if infraUnavailable(out) {
			return infraf("psql недоступен: %s", firstLine(out))
		}
		return fmt.Errorf("psql: %w\n%s", err, out)
	}
	fmt.Println("OK: схема применена:", schemaPath)
	return nil
}

// dockerReady — отвечает ли демон Docker.
func dockerReady() bool {
	return exec.Command("docker", "info").Run() == nil
}

// composeServiceID — id контейнера сервиса ("" — сервис не создан).
func composeServiceID(compose, service string) string {
	out, err := exec.Command("docker", "compose", "-f", compose, "ps", "-q", service).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// infraUnavailable — ошибка вызвана инфраструктурой (нет демона Docker,
// сервис не запущен, БД не отвечает или отказала в аутентификации), а не
// кодом: такие падения — INFRA_DEGRADED, а не дефект.
func infraUnavailable(output string) bool {
	s := strings.ToLower(output)
	for _, marker := range []string{
		"cannot connect to the docker daemon",
		"docker: not found",
		"docker: command not found",
		"executable file not found",
		"is not running",
		"no such container",
		"connection refused",
		"password authentication failed",
		"could not connect to server",
		"the database system is starting up",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// firstLine — первая непустая строка вывода (для коротких сообщений).
func firstLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return strings.TrimSpace(out)
}

// pgUser/pgDB — учётные данные Postgres песочницы (те же имена переменных,
// что в compose-файле провижининга).
func pgUser() string {
	if v := strings.TrimSpace(os.Getenv("SANDBOX_PG_USER")); v != "" {
		return v
	}
	return "postgres"
}

func pgDB() string {
	if v := strings.TrimSpace(os.Getenv("SANDBOX_PG_DB")); v != "" {
		return v
	}
	return "postgres"
}

// waitAvailable ждёт, пока Postgres и MinIO станут доступны по факту (проба
// портов), с общим таймаутом.
func waitAvailable(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		caps := sandbox.DetectCapabilities()
		if caps.Postgres == sandbox.StateAvailable && caps.MinIO == sandbox.StateAvailable {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("истёк таймаут ожидания Postgres/MinIO (%s): %s", timeout, caps.Summary())
		}
		time.Sleep(time.Second)
	}
}

// provisionTimeout — таймаут ожидания готовности стека (SANDBOX_PROVISION_TIMEOUT
// в секундах, по умолчанию 90).
func provisionTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("SANDBOX_PROVISION_TIMEOUT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 90 * time.Second
}

// envLines формирует export-строки SANDBOX_HAS_* по фактической доступности
// возможностей. Отдельная чистая функция — ради герметичных тестов.
func envLines(caps sandbox.Capabilities, compose string) []string {
	lines := []string{
		fmt.Sprintf("export SANDBOX_HAS_DOCKER=%d", boolBit(caps.Docker == sandbox.StateAvailable)),
		fmt.Sprintf("export SANDBOX_HAS_POSTGRES=%d", boolBit(caps.Postgres == sandbox.StateAvailable)),
		fmt.Sprintf("export SANDBOX_HAS_MINIO=%d", boolBit(caps.MinIO == sandbox.StateAvailable)),
	}
	if abs, err := filepath.Abs(compose); err == nil {
		lines = append(lines, "export SANDBOX_COMPOSE_FILE="+abs)
	}
	return lines
}

func boolBit(b bool) int {
	if b {
		return 1
	}
	return 0
}
