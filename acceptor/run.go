package acceptor

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"ai/sandbox"
)

// runCommand запускает команду через sh -c в указанной директории и собирает
// объединённый stdout+stderr, код выхода и признак превышения таймаута.
//
// Возвращаемая ошибка — это ошибка запуска процесса либо сигнал таймаута.
// Ненулевой код выхода НЕ превращается в ошибку: он отдаётся через exitCode,
// чтобы приёмщик мог отличать «процесс упал» от «процесс не стартовал».
//
// Если каталог покрыт активной сессионной песочницей (Этап 1), команда идёт
// в контейнер сессии; имя проекта приёмщик не знает, поэтому матчинг по пути
// (пустое имя в Lookup = наилучшее покрытие). Нет сессии — прежний хостовый
// путь: приёмка не должна падать из-за отсутствия контейнера.
//
// Команда исполняется в собственной группе процессов (Setpgid), а по таймауту
// завершается ВСЯ группа: многие запуски порождают дочерние процессы
// (например «go run .» — скомпилированный бинарь), которые наследуют каналы
// stdout/stderr. Если убивать только непосредственного ребёнка (sh), выживший
// потомок держит канал открытым и cmd.Wait() блокируется навсегда.
func runCommand(dir, command string, timeout time.Duration) (output string, exitCode int, timedOut bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Сессионный контейнер — приоритетный исполнитель (см. шапку).
	if ws := sandbox.Lookup("", dir); ws != nil {
		res, runErr := ws.Run(ctx, dir, command)
		if runErr != nil {
			return res.Stdout, -1, false, runErr
		}
		if res.TimedOut {
			// Таймаут контейнера может сработать чуть раньше клиентского
			// дедлайна — ошибка таймаута обязана быть непустой в обоих случаях.
			if ctx.Err() != nil {
				return res.Stdout, -1, true, ctx.Err()
			}
			return res.Stdout, -1, true, context.DeadlineExceeded
		}
		return res.Stdout + res.Stderr, res.ExitCode, false, res.ExitErr
	}

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
		// Превышен таймаут: процесс (и его потомки) ещё жив — принудительно
		// завершаем всю группу процессов, чтобы закрылись унаследованные
		// каналы вывода и cmd.Wait() не завис навсегда.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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
