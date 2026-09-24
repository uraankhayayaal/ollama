package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadFilesMissingFileHint: чтение несуществующего файла возвращает
// status=error, hint=true, retry=forbidden и подсказку с запретом повторного
// вызова (контракт BUG-01-T1).
func TestReadFilesMissingFileHint(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	raw, err := ops.ReadFiles(map[string]any{"filenames": []string{"no/such/file.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	var results []map[string]string
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatalf("разбор ответа: %v (%s)", err, raw)
	}
	if len(results) != 1 {
		t.Fatalf("ожидали 1 ответ, got %d: %s", len(results), raw)
	}
	r := results[0]
	if r["status"] != "error" {
		t.Errorf("ожидали status=error, got %q: %#v", r["status"], r)
	}
	if r["hint"] != "true" {
		t.Errorf("ожидали hint=true, got %q: %#v", r["hint"], r)
	}
	if r["retry"] != "forbidden" {
		t.Errorf("ожидали retry=forbidden, got %q: %#v", r["retry"], r)
	}
	if !strings.Contains(r["message"], "не существует") {
		t.Errorf("подсказка должна содержать «не существует»: %#v", r)
	}
	if !strings.Contains(r["message"], "List") {
		t.Errorf("подсказка должна рекомендовать инструмент List: %#v", r)
	}
}

// TestReadFilesMissingFileRepeatLimit: повторные попытки чтения того же
// несуществующего файла ведут счётчик и на второй и далее попытках
// подсказка явно запрещает повторные вызовы.
func TestReadFilesMissingFileRepeatLimit(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	first, err := ops.ReadFiles(map[string]any{"filenames": []string{"ghost.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	var firstRes []map[string]string
	if err := json.Unmarshal(first, &firstRes); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}
	if firstRes[0]["retry"] != "forbidden" {
		t.Errorf("первая попытка: ожидали retry=forbidden: %#v", firstRes[0])
	}

	second, err := ops.ReadFiles(map[string]any{"filenames": []string{"ghost.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	var secondRes []map[string]string
	if err := json.Unmarshal(second, &secondRes); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}
	if secondRes[0]["retry"] != "forbidden" {
		t.Errorf("повторная попытка: ожидали retry=forbidden: %#v", secondRes[0])
	}
	if !strings.Contains(secondRes[0]["message"], "повторная попытка") {
		t.Errorf("повторная попытка должна помечаться в подсказке: %#v", secondRes[0])
	}
}

// TestReadFilesMissingDir: чтение директории (а не файла) — ошибка без
// hint/retry (это не «несуществующий файл», а неверный тип пути).
func TestReadFilesMissingDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	ops := &FileOps{OutputDir: dir}

	raw, err := ops.ReadFiles(map[string]any{"filenames": []string{"subdir"}})
	if err != nil {
		t.Fatal(err)
	}
	var results []map[string]string
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatalf("разбор ответа: %v (%s)", err, raw)
	}
	if len(results) != 1 {
		t.Fatalf("ожидали 1 ответ, got %d: %s", len(results), raw)
	}
	r := results[0]
	if r["status"] != "error" {
		t.Errorf("ожидали status=error, got %q: %#v", r["status"], r)
	}
	if r["hint"] == "true" {
		t.Errorf("для директории hint не должен быть true: %#v", r)
	}
	if r["retry"] == "forbidden" {
		t.Errorf("для директории retry не должен быть forbidden: %#v", r)
	}
	if !strings.Contains(r["message"], "директория") {
		t.Errorf("ожидали сообщение о директории: %#v", r)
	}
}
