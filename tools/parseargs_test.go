package tools

import (
	"fmt"
	"testing"
)

// Модели регулярно присылают почти-валидный JSON. Раньше любой дефект убивал
// весь запуск («invalid character ']' looking for beginning of value»).
func TestParseArgumentsToleratesModelJSON(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"валидный", `{"command":"go build ./..."}`, "go build ./..."},
		{"висячая запятая", `{"command":"go test ./...",}`, "go test ./..."},
		{"мусор после объекта", "```json\n{\"command\":\"make test\"}\n```", "make test"},
		{"пояснение после JSON", `{"command":"go vet"} — вот команда`, "go vet"},
		{"одинарные кавычки", `{'command':'go build'}`, "go build"},
		{"перевод строки в строке", "{\"command\":\"go test \\\"all\\\"\nвторой\"}", "go test \"all\"\nвторой"},
		{"умные кавычки", "{\u201ccommand\u201d: \u201cgo test\u201d}", "go test"},
		{"пусто", `   `, ""},
	}
	for _, tc := range cases {
		args, err := ParseArguments(tc.raw)
		if err != nil {
			t.Fatalf("%s: ParseArguments(%q) вернул ошибку %v", tc.name, tc.raw, err)
		}
		if tc.want == "" {
			if len(args) != 0 {
				t.Fatalf("%s: ожидались пустые аргументы, got %v", tc.name, args)
			}
			continue
		}
		got := fmt.Sprint(args["command"])
		if got != tc.want {
			t.Fatalf("%s: command = %q, ожидалось %q", tc.name, got, tc.want)
		}
	}
}

// Совсем нечитаемый вход честно возвращает ошибку разбора — вызывающий код
// сообщит модели, что нужно повторить вызов.
func TestParseArgumentsKeepsRealError(t *testing.T) {
	if _, err := ParseArguments(`{"command":`); err == nil {
		t.Fatal("оборванный JSON должен возвращать ошибку разбора")
	}
}

// Вывод терминала нормализуется до попадания в JSON-пакет и в историю модели.
func TestSanitizeToolOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"цвета ANSI", "\x1b[0;32mFAIL\x1b[0m test", "FAIL test"},
		{"OSC-гиперссылка", "текст\x1b]8;;http://x\x07ссылка\x1b]8;;\x07", "текстссылка"},
		{"CRLF", "a\r\nb", "a\nb"},
		{"NUL и C1", "a\x00b\x9bc", "ab\ufffdc"},
		{"табуляция и перевод строки", "a\tb\nc", "a\tb\nc"},
		{"пусто", "", ""},
	}
	for _, tc := range cases {
		if got := SanitizeToolOutput(tc.in); got != tc.want {
			t.Fatalf("%s: SanitizeToolOutput(%q) = %q, ожидалось %q", tc.name, tc.in, got, tc.want)
		}
	}
}
