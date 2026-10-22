package acceptor

import (
	"os/exec"
	"testing"
)

// TestStripMakeServiceLines — служебные сообщения make не являются выводом
// команды: GNU Make печатает «Entering/Leaving directory» и «Nothing to be
// done for …», когда запускается как под-make (MAKELEVEL>0 — например, когда
// тесты идут через «make backend-test»). Без фильтрации приёмка считала бы
// такой вывод нарушением форматирования или ошибкой анализатора.
func TestStripMakeServiceLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "под-make: служебные строки вырезаются",
			in:   "make[1]: Entering directory '/tmp/proj'\nmake[1]: Leaving directory '/tmp/proj'\n",
			want: "",
		},
		{
			name: "цель без рецепта: nothing to be done",
			in:   "make: Nothing to be done for 'lint'.\n",
			want: "",
		},
		{
			name: "цель актуальна: is up to date",
			in:   "make: 'build' is up to date.\n",
			want: "",
		},
		{
			name: "находки форматирования сохраняются",
			in:   "main.go\nutils.go\n",
			want: "main.go\nutils.go\n",
		},
		{
			name: "ошибка сборки сохраняется",
			in:   "main.go:12: undefined: foo\n",
			want: "main.go:12: undefined: foo\n",
		},
		{
			name: "строка про make в тексте ошибки не вырезается",
			in:   "main.go:12: undefined: make\n",
			want: "main.go:12: undefined: make\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripMakeServiceLines(tc.in); got != tc.want {
				t.Fatalf("stripMakeServiceLines(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAcceptMakefileFormatAnalyzeInSubmake — make-цель форматирования без
// нарушений должна считаться пройденной, даже когда make запущен как
// под-make (MAKELEVEL>0). GNU Make 4.x в этом режиме печатает служебные
// строки, которые не должны превращаться в «нарушения форматирования».
func TestAcceptMakefileFormatAnalyzeInSubmake(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make не установлен")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go не установлен")
	}
	// Под-make: make считает себя вложенным и печатает служебные строки.
	t.Setenv("MAKELEVEL", "1")

	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module submake\n")
	writeTestFile(t, dir, "main.go", `package main

func main() { println("привет") }
`)
	writeMakefile(t, dir, `build:
	@go build ./...

lint:
	@true

test:
	@true

run:
	@echo run ok
`)

	cfg := DefaultConfig()
	cfg.BuildTimeout = 2 * testTimeout(t)
	cfg.RunTimeout = testTimeout(t)

	rep := Accept(dir, cfg)
	if rep.Format == nil || rep.Format.Command != "make lint" || !rep.Format.OK {
		t.Fatalf("формат через make lint: got %#v", rep.Format)
	}
	if rep.Analyze == nil || rep.Analyze.Command != "make test" || !rep.Analyze.OK {
		t.Fatalf("анализ через make test: got %#v", rep.Analyze)
	}
}
