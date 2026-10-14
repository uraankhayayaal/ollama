// Привязка песочницы к порту домена (Этап 4, срез E4).
//
// `infrastructure/sandbox` — бывший пакет `sandbox/` (хост- и
// контейнер-исполнители команд агента). Порт
// `domain/services.SandboxWorkspace` объявляет изолированное окружение
// выполнения команд; реализации `*LocalWorkspace` и `*DockerWorkspace`
// удовлетворяют ему напрямую, потому что `Result`/`Workspace` — alias'ы
// канонических типов домена (см. `workspace.go`). Адаптер не нужен.
//
// Что НЕ входит в порт: файловые операции (чтение/запись остаются на хосте —
// рабочие каталоги монтируются bind-mount'ом), управление жизненным циклом
// контейнера сессии (`SessionOptions`, `StopSession`) и низкоуровневый клиент
// Docker Engine API (`Engine`) — это инфраструктурные детали пакета.

package sandbox

import "ai/domain/services"

// Compile-time проверки: реализации песочницы удовлетворяют порту домена.
var (
	_ services.SandboxWorkspace = (*LocalWorkspace)(nil)
	_ services.SandboxWorkspace = (*DockerWorkspace)(nil)
)
