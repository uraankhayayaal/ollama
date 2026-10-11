package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsTestPath — распознавание тестовых файлов по конвенциям стеков.
func TestIsTestPath(t *testing.T) {
	cases := map[string]bool{
		// Go
		"internal/order/service_test.go": true,
		"main.go":                        false,
		// JS/TS
		"src/pages/Login.test.tsx":       true,
		"src/App.spec.js":                true,
		"src/components/Button.spec.mts": true,
		"src/App.tsx":                    false,
		// Python
		"app/test_views.py": true,
		"app/views_test.py": true,
		"app/views.py":      false,
		// каталоги
		"tests/api.spec.js":     true,
		"__tests__/helpers.ts":  true,
		"e2e/login.spec.ts":     true,
		"testdata/golden.json":  true,
		"internal/test/foo.go":  true,
		"contest/java/Foo.java": false, // «contest» не каталог test
		// конфиги раннеров
		"playwright.config.ts": true,
		"jest.config.js":       true,
		"pytest.ini":           true,
		"conftest.py":          true,
		// CamelCase-конвенции (без ложного «contest»/«unit»)
		"src/OrderTest.java": true,
		"src/OrderTests.kt":  true,
		"src/OrderTest.cs":   true,
		"src/OrderTest.php":  true,
		"src/contest.java":   false,
		"src/unit.php":       false,
		// обычный код
		"server/internal/api/handler.go": false,
		"README.md":                      false,
	}
	for rel, want := range cases {
		if got := isTestPath(rel); got != want {
			t.Errorf("isTestPath(%q) = %v, хотим %v", rel, got, want)
		}
	}
}

// TestWriteTestsOnlyBlocksProductCode — в режиме «только тесты» запись кода
// продукта отклоняется, а тестов и дополнительных путей (отчёт) — разрешена.
func TestWriteTestsOnlyBlocksProductCode(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	ops.SetWriteTestsOnly([]string{"TEST_REPORT.md"})

	ok := []string{"main_test.go", "src/api.test.ts", "TEST_REPORT.md", "playwright.config.ts"}
	for _, f := range ok {
		if err := ops.Write(f, "x"); err != nil {
			t.Errorf("Write(%q) должен быть разрешён: %v", f, err)
		}
	}
	bad := []string{"main.go", "src/App.tsx", "internal/order/service.go", "package.json"}
	for _, f := range bad {
		if err := ops.Write(f, "x"); err == nil {
			t.Errorf("Write(%q) кода продукта должен быть отклонён", f)
		} else if !strings.Contains(err.Error(), "только тесты") {
			t.Errorf("сообщение отказа для %q должно объяснять ограничение, got: %v", f, err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); statErr == nil {
			t.Errorf("файл %q не должен был появиться на диске", f)
		}
	}
}

// TestWriteTestsOnlyDelete — удаление кода продукта в режиме «только тесты»
// тоже запрещено (DeleteFiles ходит через тот же writeAllowed).
func TestWriteTestsOnlyDelete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}
	ops := &FileOps{OutputDir: dir}
	ops.SetWriteTestsOnly(nil)

	if err := ops.Write("gone_test.go", "package x"); err != nil {
		t.Fatalf("тест должен писаться: %v", err)
	}
	res, err := ops.WriteFiles(map[string]any{
		"files": []map[string]string{{"filename": "main.go", "content": "package main"}},
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	if !strings.Contains(string(res), `"status":"error"`) {
		t.Fatalf("перезапись main.go должна быть отклонена, got: %s", string(res))
	}
	removed, err := ops.Remove("main.go")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if removed["status"] != "error" {
		t.Fatalf("удаление main.go должно быть отклонено, got: %#v", removed)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "main.go")); statErr != nil {
		t.Fatalf("main.go не должен быть удалён: %v", statErr)
	}
}
