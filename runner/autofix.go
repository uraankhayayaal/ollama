package runner

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

// AutoFixer — необязательный интерфейс агента для координации пост-раундовых
// хуков (Ф-2 + Ф-5): единый сегмент очереди затронутых файлов + LSP-проверка.
// Реализуется *tools.FileOps (встроен в агентов-разработчиков). TakeTouched
// дренит очередь РОВНО ОДИН раз за раунд; полученный список раннер раздаёт
// включённым хукам — авто-лечению (LspCheckFiles) и переиндексации RAG
// (Reindexer) — чтобы хуки не конкурировали за одну очередь.
// autoFixEnabled сообщает, включено ли авто-самоисправление (LSP_AUTO_FIX).
func autoFixEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(autoFixOnEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// autoFixMaxRounds — лимит подряд итераций авто-лечения (LSP_MAX_FIX_ROUNDS),
// по умолчанию defaultAutoFixMax. Некорректное/нулевое значение — дефолт.
func autoFixMaxRounds() int {
	if v := strings.TrimSpace(os.Getenv(autoFixMaxRoundsEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultAutoFixMax
}

// filterNewDiags — алиас usecases.FilterNewDiags (политика в application).
func filterNewDiags(diags []string, sent map[string]bool) []string {
	return usecases.FilterNewDiags(diags, sent)
}

// autoFixMessage — алиас usecases.AutoFixMessage (политика в application).
func autoFixMessage(diags []string, iteration, max int) string {
	return usecases.AutoFixMessage(diags, iteration, max)
}
