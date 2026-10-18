package tools

// Гард «конкретное замечание QA» (инцидент A7-04): возврат задачи из
// «На тестирование» (testing) в «В работе» (in_progress) — это откат работы
// разработчику. Возврат без внятного дефекта превращается в ping-pong
// testing ↔ in_progress и сжигает токены (см. docs/plans/PLAN-2026-10-18-A7-04-system-fix.md).
//
// Правило: QA обязан приложить конкретную причину — ссылку на файл:строку,
// путь, команду воспроизведения или описание шагов. Общие формулировки
// («нужна доработка», «есть замечания») не принимаются.

import (
	"fmt"
	"regexp"
	"strings"
)

// defectReasonMinRunes — минимальная длина осмысленной причины.
const defectReasonMinRunes = 20

// concreteDefectPatterns — маркеры конкретного дефекта:
//   - ссылка на файл с расширением (внутри — точка в пути);
//   - ссылка на строку (path:42);
//   - команда воспроизведения/проверки;
//   - явное описание шагов воспроизведения.
var concreteDefectPatterns = []*regexp.Regexp{
	regexp.MustCompile(`[A-Za-z0-9_./\\-]+\.(go|ts|tsx|js|jsx|py|rs|java|kt|sql|ya?ml|json|toml|md|sh|css|html|gradle|mod|sum)`),
	regexp.MustCompile(`[A-Za-z0-9_./\\-]+:\d+`),
	regexp.MustCompile(`(?i)\b(go (test|build|vet|run)|make |npm |pnpm |yarn |pytest|cargo |docker compose|migrate)\b`),
	regexp.MustCompile(`(?i)(воспроизв|репро|repro|steps to reproduce|шаги)`),
}

// validateQaRevertReason проверяет, что причина возврата testing → in_progress
// действительно конкретна. Возвращает nil, если причина приемлема.
func validateQaRevertReason(reason string) error {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return fmt.Errorf("для возврата testing → in_progress обязателен reason: опиши конкретный дефект (файл:строка, путь, команда воспроизведения или шаги). Общее «нужна доработка» не принимается")
	}
	if n := len([]rune(trimmed)); n < defectReasonMinRunes {
		return fmt.Errorf("reason слишком короткий (%d символов): укажи конкретный дефект (файл:строка, команда воспроизведения или шаги), а не общую формулировку", n)
	}
	for _, re := range concreteDefectPatterns {
		if re.MatchString(trimmed) {
			return nil
		}
	}
	return fmt.Errorf("reason не содержит конкретики: добавь ссылку на файл:строку, команду воспроизведения (go test/go build/…) или шаги. Возврат без дефекта крутит testing ↔ in_progress и сжигает бюджет")
}
