package adapters

// Ф-2: авто-самоисправление в цикле Generate.
//
// Агент-разработчик (через встроенный *tools.FileOps) реализует интерфейс
// AutoFixer. После раунда, в котором были мутации файлов, раннер прогоняет
// LspCheck по затронутым файлам. Если есть ошибки — в диалог подмешивается
// СКРЫТЫЙ user-промпт с точными строками и модель правит код, не тратя раунды
// на перечитывание сырых логов сборки. Число подряд итераций ограничено
// LSP_MAX_FIX_ROUNDS; чистое (без ошибок) срабатывание сбрасывает счётчик.
// Поведение управляется LSP_AUTO_FIX (по умолчанию включено; 0/false/off/no —
// выключить). При отсутствии чекеров LspCheck деградирует в skipped, диагностик
// нет — фича тихо неактивна.
//
// Адаптер порта `AutoFixer`: политика цикла (что и как подмешивать) живёт в
// application/usecases, здесь — только конфигурация из окружения.

import (
	"os"
	"strconv"
	"strings"

	"ai/application/usecases"
)

// AutoFixer — порт агента с очередью затронутых файлов и LSP-диагностикой
// (usecases.AutoFixer).
type AutoFixer = usecases.AutoFixer

const (
	autoFixOnEnv        = "LSP_AUTO_FIX"
	autoFixMaxRoundsEnv = "LSP_MAX_FIX_ROUNDS"
	defaultAutoFixMax   = 3
)

// AutoFixEnabled сообщает, включено ли авто-самоисправление (LSP_AUTO_FIX).
// По умолчанию — да; выключается 0/false/off/no.
func AutoFixEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(autoFixOnEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// AutoFixMaxRounds — лимит подряд итераций авто-лечения (LSP_MAX_FIX_ROUNDS),
// по умолчанию defaultAutoFixMax. Некорректное/нулевое значение — дефолт.
func AutoFixMaxRounds() int {
	if v := strings.TrimSpace(os.Getenv(autoFixMaxRoundsEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultAutoFixMax
}
