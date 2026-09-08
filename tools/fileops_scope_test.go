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
