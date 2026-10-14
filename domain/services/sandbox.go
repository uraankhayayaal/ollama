package services

import "context"

// SandboxWorkspace — порт изолированного окружения выполнения команд агента.
//
// Одна сессия оркестрации получает одно окружение (Workspace): команды
// выполняются в нём, а по завершении оно освобождается. Реализации живут в
// инфраструктуре (`infrastructure/sandbox`: LocalWorkspace — хост,
// DockerWorkspace — контейнер сессии); application/tools-слой зависит только
// от этого интерфейса.
//
// Контракт Run: он не должен висеть дольше дедлайна ctx (по дедлайну команда
// останавливается и результат помечается TimedOut). Ошибку Run возвращает
// только за инфраструктурные сбои (нет демона, каталог вне монтирования);
// ненулевой выход команды — это поле результата, а не ошибка.
type SandboxWorkspace interface {
	Run(ctx context.Context, workdir, command string) (SandboxResult, error)
	Close(ctx context.Context) error
}

// SandboxResult — результат выполнения одной команды в песочнице.
type SandboxResult struct {
	Stdout string
	Stderr string
	// ExitCode — код выхода; -1, если неизвестен (не стартовал / убит по
	// таймауту до получения кода).
	ExitCode int
	// TimedOut — команда остановлена по таймауту (не путать с ненулевым
	// выходом: это разные состояния для модели).
	TimedOut bool
	// ExitErr — ошибка завершения (ненулевой выход). nil при успехе. Ошибки
	// ЗАПУСКА (docker недоступен, каталог не найден) возвращаются отдельной
	// ошибкой Run.
	ExitErr error
}
