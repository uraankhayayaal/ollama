package acceptor

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// runCommand запускает команду через sh -c в указанной директории и собирает
// объединённый stdout+stderr, код выхода и признак превышения таймаута.
//
// Возвращаемая ошибка — это ошибка запуска процесса либо сигнал таймаута.
// Ненулевой код выхода НЕ превращается в ошибку: он отдаётся через exitCode,
// чтобы приёмщик мог отличать «процесс упал» от «процесс не стартовал».
func runCommand(dir, command string, timeout time.Duration) (output string, exitCode int, timedOut bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if serr := cmd.Start(); serr != nil {
		return buf.String(), -1, false, serr
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		// Превышен таймаут: процесс ещё жив — принудительно завершаем.
		_ = cmd.Process.Kill()
		<-done
		return buf.String(), -1, true, ctx.Err()
	case werr := <-done:
		code := 0
		if werr != nil {
			if ee, ok := werr.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		return buf.String(), code, false, werr
	}
}

// trimOutput обрезает длинный вывод до cfg.MaxLog символов с пометкой
// обрезания, чтобы не раздувать отчёт и контекст модели.
func trimOutput(out string, max int) string {
	if max <= 0 || len(out) <= max {
		return out
	}
	return strings.TrimSpace(out[:max]) + "\n[... вывод обрезан ...]"
}