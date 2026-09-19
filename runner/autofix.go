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
	"fmt"
	"os"
	"strconv"
	"strings"
)

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
type AutoFixer interface {
	// TakeTouched забирает и очищает очередь файлов, затронутых мутацией
	// с прошлого вызова.
	TakeTouched() []string
	// LspCheckFiles выполняет LspCheck по явному списку файлов и возвращает
	// строки диагностик ("file:line:col: message"). hadMutation=true, если
	// список непуст (файлы менялись).
	LspCheckFiles(files []string) (diagnostics []string, hadMutation bool)
}

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

// filterNewDiags возвращает только те строки диагностик, которых ещё не было
// среди отправленных (sent), сохраняя порядок. Нужен, чтобы не подмешивать
// модели один и тот же текст повторно между итерациями (токен-бюджет, Ф-4).
func filterNewDiags(diags []string, sent map[string]bool) []string {
	if len(diags) == 0 || len(sent) == 0 {
		return diags
	}
	fresh := make([]string, 0, len(diags))
	for _, d := range diags {
		if !sent[d] {
			fresh = append(fresh, d)
		}
	}
	return fresh
}

// autoFixMessage составляет скрытый user-промпт «исправь код» с точными
// строками ошибок и номером итерации.
func autoFixMessage(diags []string, iteration, max int) string {
	var b strings.Builder
	b.WriteString("Твоя последняя правка файлов вызвала ошибки компиляции или типов (LspCheck по затронутым файлам). ")
	b.WriteString("Исправь код на основе этих точных строк:\n")
	for _, d := range diags {
		fmt.Fprintf(&b, "- %s\n", d)
	}
	fmt.Fprintf(&b, "Исправь и продолжай цикл. Итерация %d/%d.", iteration, max)
	return b.String()
}
