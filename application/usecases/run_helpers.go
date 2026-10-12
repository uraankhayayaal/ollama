package usecases

import (
	"fmt"
	"strings"
)

// AppLogFeedMaxLines — сколько строк лога уходит в промпт. Логи рантайма
// самые шумные (HTTP-запросы, прогресс-бары сборки), и 2000 строк съели бы
// контекст раунда целиком.
const AppLogFeedMaxLines = 60

// AppLogFeedMaxChars — потолок по символам: одна строка трейсера Go с
// locals бывает в десятки килобайт.
const AppLogFeedMaxChars = 6000

// FilterNewDiags возвращает только те строки диагностик, которых ещё не было
// среди отправленных (sent), сохраняя порядок. Нужен, чтобы не подмешивать
// модели один и тот же текст повторно между итерациями (токен-бюджет, Ф-4).
func FilterNewDiags(diags []string, sent map[string]bool) []string {
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

// AutoFixMessage составляет скрытый user-промпт «исправь код» с точными
// строками ошибок и номером итерации.
func AutoFixMessage(diags []string, iteration, max int) string {
	var b strings.Builder
	b.WriteString("Твоя последняя правка файлов вызвала ошибки компиляции или типов (LspCheck по затронутым файлам). ")
	b.WriteString("Исправь код на основе этих точных строк:\n")
	for _, d := range diags {
		fmt.Fprintf(&b, "- %s\n", d)
	}
	fmt.Fprintf(&b, "Исправь и продолжай цикл. Итерация %d/%d.", iteration, max)
	return b.String()
}

// TrimAppLogLines оставляет последние AppLogFeedMaxLines строк и обрезает по
// символам. Обрезка идёт с начала внутри лимита строк: при переполнении
// символами начало лога неинтересно, а конец содержит причину.
func TrimAppLogLines(lines []string) []string {
	if len(lines) > AppLogFeedMaxLines {
		lines = lines[len(lines)-AppLogFeedMaxLines:]
	}
	budget := AppLogFeedMaxChars
	for i, l := range lines {
		if len(l) > budget {
			return lines[i:]
		}
		budget -= len(l) + 1
	}
	return lines
}

// AppLogFeedMessage собирает скрытый user-промпт по логам рантайма.
func AppLogFeedMessage(source string, lines []string, used, max int) string {
	var b strings.Builder
	b.WriteString("ПРИЛОЖЕНИЕ В РАНТАЙМЕ ВЫДАЛО ЛОГИ (инструмент ReadAppLogs, источник ")
	b.WriteString(orDefault(source, "local"))
	b.WriteString("). Это не сообщение об ошибке твоей работы — это фактическое поведение запущенного приложения. ")
	b.WriteString("Осмотри строки ниже и, если в них есть настоящая причина (исключение, «address already in use», ")
	b.WriteString("ошибка миграции/БД, неожиданный код ответа, падение при старте), исправь её в коде ДО того, как объявишь задачу выполненной. ")
	b.WriteString("Если логи чистые, просто продолжай работу. Не пересказывай логи в ответе и не вызывай ReadAppLogs повторно ")
	b.WriteString("ради тех же строк — они уже у тебя есть.\n\n")
	b.WriteString("--- логи рантайма ---\n")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n--- конец логов ---")
	fmt.Fprintf(&b, "\n(подсказка %d из %d)", used, max)
	return b.String()
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
