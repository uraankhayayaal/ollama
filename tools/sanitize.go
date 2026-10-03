package tools

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Санитайзер вывода команд. Вывод терминала попадает в пайплайн LLM буквально:
// escape-последовательности ANSI, NUL-байты и прочие управляющие символы
// деформируют JSON-пакеты (в т.ч. при разборе аргументов вызовов на
// следующем раунде) и сбивают саму оркестрацию: «invalid character ']'
// looking for beginning of value». Поэтому любой stdout/stderr нормализуется
// ДО того, как попасть в историю диалога или в отчёт.
var (
	// ansiCSI — escape-последовательности вида ESC[0;31m (цвета, курсор,
	// очистка экрана), включая « OSC-8 гиперссылки.
	ansiCSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	// ansiOSC — ESC]…BEL / ESC]…ESC\ (заголовки терминала, OSC-8 ссылки).
	ansiOSC = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	// ansiOther — прочие двухсимвольные последовательности (ESC=, ESC>).
	ansiOther = regexp.MustCompile(`\x1b[=>78MDEc]`)
)

// SanitizeToolOutput приводит вывод команды к безопасному виду для LLM:
// убирает цвета и управляющие последовательности терминала, нормализует
// переводы строк и выкидывает непечатаемые байты (кроме перевода строки и
// табуляции). Невалидный UTF-8 заменяется на U+FFFD — иначе байты доходят до
// JSON-пакета и ломают разбор.
func SanitizeToolOutput(s string) string {
	if s == "" {
		return s
	}
	s = ansiOSC.ReplaceAllString(s, "")
	s = ansiCSI.ReplaceAllString(s, "")
	s = ansiOther.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == utf8.RuneError && size == 1:
			// Невалидный UTF-8: в JSON-пакет такие байты попадать не должны,
			// но и молча их терять нельзя — заменяем на видимый маркер.
			b.WriteRune(utf8.RuneError)
		case r == utf8.RuneError:
			// Реальный U+FFFD из исходных данных — оставляем.
			b.WriteRune(r)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			// Управляющие и C1-символы терминала — в LLM не идут.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
