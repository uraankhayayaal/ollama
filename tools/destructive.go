package tools

// destructive.go — обёртка над sandbox.DestructiveReason (Этап 1, п. 1.4).
//
// Таблица запретов перенесена в пакет sandbox: её проверяют оба исполнителя
// (локальный и контейнерный), а tools зависит от sandbox, но не наоборот.
// Имя destructiveCommandReason сохранено — на него завязаны вызовы
// (runCommandSandbox, sandboxSpecFor) и существующие тесты.

import "ai/sandbox"

// destructiveCommandReason возвращает причину запрета, если команда
// разрушительна. Пустая строка — команда разрешена.
func destructiveCommandReason(command string) (string, bool) {
	return sandbox.DestructiveReason(command)
}
