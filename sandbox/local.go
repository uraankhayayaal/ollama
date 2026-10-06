package sandbox

// local.go — локальный исполнитель команд: прежнее поведение FileOps на хосте
// (п. 1.2), вынесенное за интерфейс Workspace.
//
// Команда выполняется через sh -c в workdir, в собственной группе процессов
// (Setpgid): команда может порождать детей (серверы, npm), а по таймауту
// убивается ВСЯ группа — выживший потомок держал бы каналы stdout/stderr
// открытыми, и cmd.Wait() блокировался бы навсегда.

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
)

// LocalWorkspace — исполнение команд на хосте без изоляции.
type LocalWorkspace struct {
	// Command — подмена исполнителя: продакшн nil (тогда sh -c), в
	// hermetic-тестах — заглушка, чтобы тесты не трогали shell.
	Command func(command, workdir string) (*exec.Cmd, error)
}

// Run выполняет command на хосте. Ошибка возвращается только при сбое
// ЗАПУСКА (не смогли стартовать процесс); ненулевой выход — это Result.
func (w *LocalWorkspace) Run(ctx context.Context, workdir, command string) (Result, error) {
	var cmd *exec.Cmd
	var err error
	if w != nil && w.Command != nil {
		cmd, err = w.Command(command, workdir)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	if err != nil {
		return Result{}, err
	}
	cmd.Dir = workdir
	// День: группа процессов убивается по таймауту целиком (см. шапку файла).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return Result{}, err
	}

	// Ждём завершения в отдельной горутине: при таймауте необходимо убить
	// ВСЮ группу процессов, иначе cmd.Wait() (в done) не разблокируется.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	res := Result{ExitCode: -1}
	select {
	case <-ctx.Done():
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
		res.TimedOut = true
		res.ExitErr = ctx.Err()
	case werr := <-done:
		res.ExitErr = werr
	}
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	if ee, ok := res.ExitErr.(*exec.ExitError); ok {
		res.ExitCode = ee.ExitCode()
	}
	if res.ExitErr == nil {
		res.ExitCode = 0
	}
	return res, nil
}

// Close — на хосте нечего освобождать.
func (w *LocalWorkspace) Close(context.Context) error { return nil }
