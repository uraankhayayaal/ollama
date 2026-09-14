package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFileOpsScopeBlocksWrite проверяет, что Write не может записать файл
// вне области работы шага, а записи внутри области разрешены.
func TestFileOpsScopeBlocksWrite(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	ops.SetScope([]string{"src/service.go", "cmd/"})

	if err := ops.Write("src/service.go", "x"); err != nil {
		t.Fatalf("запись внутри scope должна быть разрешена: %v", err)
	}
	if err := ops.Write("cmd/main.go", "x"); err != nil {
		t.Fatalf("запись в cmd/ должна быть разрешена: %v", err)
	}
	if err := ops.Write("go.mod", "x"); err == nil {
		t.Fatal("запись вне scope должна вернуть ошибку")
	}
}

// TestFileOpsScopeReadBlocked проверяет, что чтение файла вне области
// вернёт статус error, а не содержимое.
func TestFileOpsScopeReadBlocked(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "secret.go"), []byte("SECRET"), 0644)

	ops := &FileOps{OutputDir: dir}
	ops.SetScope([]string{"src/"})

	res := ops.ReadResult("secret.go")
	if res["status"] != "error" {
		t.Fatalf("чтение вне scope должно быть отклонено, got: %#v", res)
	}
	if strings.Contains(res["message"], "SECRET") {
		t.Fatalf("ответ не должен содержать содержимое файла вне области: %#v", res)
	}
}

// TestFileOpsScopeListNested проверяет, что List видит вложенные файлы
// области (проходя через родительские директории вне области) и не
// показывает файлы за пределами scope.
func TestFileOpsScopeListNested(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "order"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "internal", "order", "a.go"), []byte("aa"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "internal", "b.go"), []byte("bb"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "root.go"), []byte("r"), 0644)

	ops := &FileOps{OutputDir: dir}
	ops.SetScope([]string{"internal/order/"})

	result, err := ops.List(nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	s := string(result)

	if !strings.Contains(s, "internal/order/a.go") {
		t.Errorf("List должен показывать internal/order/a.go, got: %s", s)
	}
	if strings.Contains(s, "internal/b.go") {
		t.Errorf("List не должен показывать internal/b.go (вне scope), got: %s", s)
	}
	if strings.Contains(s, "root.go") {
		t.Errorf("List не должен показывать root.go (вне scope), got: %s", s)
	}
}

// TestWriteFilesAcceptsFileMap проверяет, что WriteFiles принимает объектную
// форму {"files": {"путь": "контент"}} (а не только массив объектов) — модели
// иногда записывают файлы именно так, и вызов не должен пропадать впустую.
func TestWriteFilesAcceptsFileMap(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	result, err := ops.WriteFiles(map[string]any{
		"files": map[string]string{
			"server/go.mod":    "module test",
			"frontend/App.tsx": "export default 1",
		},
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	s := string(result)
	if strings.Contains(s, `"status":"error"`) {
		t.Fatalf("все файлы должны записаться, got: %s", s)
	}
	if !strings.Contains(s, `"filename":"server/go.mod"`) || !strings.Contains(s, `"filename":"frontend/App.tsx"`) {
		t.Fatalf("ожидались оба файла, got: %s", s)
	}
	for _, f := range []string{"server/go.mod", "frontend/App.tsx"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("файл %s не записан: %v", f, err)
		}
	}
}

// TestWriteFilesAcceptsSingleObject проверяет форму вызова без обёртки "files":
// {"filename": "путь", "content": "код"}.
func TestWriteFilesAcceptsSingleObject(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	result, err := ops.WriteFiles(map[string]any{
		"filename": "README.md",
		"content":  "# hello",
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	if strings.Contains(string(result), `"status":"error"`) {
		t.Fatalf("файл должен записаться, got: %s", string(result))
	}
	if b, err := os.ReadFile(filepath.Join(dir, "README.md")); err != nil || string(b) != "# hello" {
		t.Fatalf("README.md должен содержать код, content=%q err=%v", b, err)
	}
}
