package codegenerator

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestCG создаёт генератор во временной директории.
func newTestCG(t *testing.T) *Codegenerator {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return &Codegenerator{OutputDir: dir}
}

func TestResolvePathNormal(t *testing.T) {
	cg := newTestCG(t)
	full, err := cg.resolvePath("models/user.go")
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	want := filepath.Join(cg.OutputDir, "models", "user.go")
	if full != want {
		t.Errorf("resolvePath = %q, ожидали %q", full, want)
	}
}

func TestResolvePathRejectsTraversal(t *testing.T) {
	cg := newTestCG(t)
	for _, bad := range []string{"../evil.go", "a/../../evil.go", "/etc/passwd", "../../../etc/passwd"} {
		if _, err := cg.resolvePath(bad); err == nil {
			t.Errorf("resolvePath(%q) должен вернуть ошибку выхода за пределы OutputDir", bad)
		}
	}
}

func TestWriteWritesIntoOutputDir(t *testing.T) {
	cg := newTestCG(t)
	if err := cg.write("sub/main.go", "package main\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(cg.OutputDir, "sub", "main.go")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("файл %q не создан: %v", want, err)
	}
	// Файл НЕ должен появиться в CWD (регрессия бага WriteFile, писавшего в CWD).
	if _, err := os.Stat("sub/main.go"); err == nil {
		t.Fatal("файл не должен быть записан относительно рабочей директории")
	}
}

func TestWriteRejectsEscape(t *testing.T) {
	cg := newTestCG(t)
	if err := cg.write("../escape.go", "x"); err == nil {
		t.Fatal("write должен отклонить путь за пределами OutputDir")
	}
}

func TestReadResult(t *testing.T) {
	cg := newTestCG(t)
	if err := cg.write("a.go", "hello"); err != nil {
		t.Fatal(err)
	}
	res := cg.readResult("a.go")
	if res["status"] != "success" || res["content"] != "hello" {
		t.Errorf("readResult = %#v, ожидали success/hello", res)
	}

	missing := cg.readResult("nope.go")
	if missing["status"] != "error" {
		t.Errorf("чтение несуществующего файла должно быть error, got %#v", missing)
	}

	escape := cg.readResult("../escaping.go")
	if escape["status"] != "error" {
		t.Errorf("чтение с выходом за пределы должно быть error, got %#v", escape)
	}
}

func TestRemoveFile(t *testing.T) {
	cg := newTestCG(t)
	if err := cg.write("temp.go", "x"); err != nil {
		t.Fatal(err)
	}
	res, err := cg.remove("temp.go")
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != "success" {
		t.Errorf("remove = %#v, ожидали success", res)
	}
	if _, err := os.Stat(filepath.Join(cg.OutputDir, "temp.go")); !os.IsNotExist(err) {
		t.Fatalf("файл не удалён: %v", err)
	}
}

func TestWriteFilesBulk(t *testing.T) {
	cg := newTestCG(t)
	out, err := cg.WriteFiles(map[string]any{
		"files": []any{
			map[string]any{"filename": "cmd/main.go", "content": "package main"},
			map[string]any{"filename": "utils/helper.go", "content": "package utils"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{filepath.Join(cg.OutputDir, "cmd", "main.go"), filepath.Join(cg.OutputDir, "utils", "helper.go")} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("пакетная запись не создала %q: %v", want, err)
		}
	}
	if string(out) == "" {
		t.Error("WriteFiles должен вернуть JSON со статусами")
	}
}

func TestWriteFilesEmptyRejected(t *testing.T) {
	cg := newTestCG(t)
	out, err := cg.WriteFiles(map[string]any{"files": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) == "" {
		t.Error("пустой список файлов должен вернуть JSON со статусом error")
	}
}

func TestRunReturnsOutput(t *testing.T) {
	cg := newTestCG(t)
	if err := cg.write("main.go", "package main\nfunc main(){}"); err != nil {
		t.Fatal(err)
	}
	out, err := cg.Run(map[string]any{"command": "ls"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(out), "main.go") {
		t.Errorf("Run должен вернуть список файлов OutputDir, got: %s", out)
	}
}

func TestCallFunctionDispatchesTools(t *testing.T) {
	cg := newTestCG(t)
	cases := []string{"WriteFiles", "WriteFile", "ReadFiles", "DeleteFiles", "Run"}
	for _, name := range cases {
		if _, err := cg.CallFunction(name, map[string]any{}); err != nil {
			t.Errorf("CallFunction(%q) не должен возвращать ошибку, got: %v", name, err)
		}
	}
}

func TestNoOverwriteBlocksRewrite(t *testing.T) {
	cg := newTestCG(t)
	cg.Config = DefaultConfig()
	cg.Config.NoOverwrite = true

	if err := cg.write("a.go", "v1"); err != nil {
		t.Fatalf("первая запись: %v", err)
	}
	if err := cg.write("a.go", "v2"); err == nil {
		t.Fatal("запись в существующий файл должна быть заблокирована при NoOverwrite")
	}
	// Содержимое не изменилось.
	res := cg.readResult("a.go")
	if res["content"] != "v1" {
		t.Errorf("содержимое изменилось несмотря на блок, got %q", res["content"])
	}
}

func TestMaxFilesLimit(t *testing.T) {
	cg := newTestCG(t)
	cg.Config = DefaultConfig()
	cg.Config.MaxFiles = 1

	if err := cg.write("a.go", "a"); err != nil {
		t.Fatal(err)
	}
	if err := cg.write("b.go", "b"); err == nil {
		t.Fatal("второй файл должен быть отклонён при MaxFiles=1")
	}
}

func TestFinalizeWritesSummary(t *testing.T) {
	cg := newTestCG(t)
	cg.Config = DefaultConfig()
	cg.Prompt = "тестовое задание"
	if err := cg.write("cmd/main.go", "package main"); err != nil {
		t.Fatal(err)
	}

	cg.Finalize()

	res := cg.readResult("SUMMARY.md")
	if res["status"] != "success" {
		t.Fatalf("SUMMARY.md не создан: %#v", res)
	}
	content := res["content"]
	for _, want := range []string{"тестовое задание", "cmd/main.go", "## Файлы"} {
		if !contains(content, want) {
			t.Errorf("SUMMARY.md не содержит %q:\n%s", want, content)
		}
	}
}

func TestFinalizeSkipsWhenDisabled(t *testing.T) {
	cg := newTestCG(t)
	cg.Config = DefaultConfig()
	cg.Config.SummaryFile = ""
	cg.Finalize()
	if _, err := os.Stat(filepath.Join(cg.OutputDir, "SUMMARY.md")); !os.IsNotExist(err) {
		t.Fatal("SUMMARY.md не должен создаваться при пустом SummaryFile")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
