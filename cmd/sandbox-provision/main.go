// Command sandbox-provision — провижининг стека Postgres + MinIO для песочницы
// авто-проверки (п.3.1 A7-04, docs/40-operations/sandbox-readiness.md).
//
// Подкоманды:
//
//	up      — `docker compose up -d --wait` и ожидание доступности Postgres/MinIO
//	down    — `docker compose down` (доп. аргументы прокидываются, напр. -v)
//	status  — состояние возможностей окружения (sandbox.DetectCapabilities)
//	env     — экспорт SANDBOX_HAS_*/SANDBOX_COMPOSE_FILE для оркестратора
//
// Путь к compose: SANDBOX_PROVISION_COMPOSE или
// infrastructure/sandbox/provision/compose.yaml от текущего каталога.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai/infrastructure/sandbox"
)

const defaultCompose = "infrastructure/sandbox/provision/compose.yaml"

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
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sandbox-provision <up|down|status|env>")
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "sandbox-provision:", err)
	os.Exit(1)
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
