package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadFilesNoLoopOnMissing: модель, зацикливающаяся на чтении
// несуществующего файла, получает на каждой попытке hint=true и
// retry=forbidden — сигнал оркестрации не повторять вызов.
func TestReadFilesNoLoopOnMissing(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	for i := 0; i < 3; i++ {
		raw, err := ops.ReadFiles(map[string]any{"filenames": []string{"loop/never/exists.txt"}})
		if err != nil {
			t.Fatal(err)
		}
		var results []map[string]string
		if err := json.Unmarshal(raw, &results); err != nil {
			t.Fatalf("разбор ответа: %v (%s)", err, raw)
		}
		if len(results) != 1 {
			t.Fatalf("попытка %d: ожидали 1 ответ, got %d", i+1, len(results))
		}
		if results[0]["hint"] != "true" || results[0]["retry"] != "forbidden" {
			t.Fatalf("попытка %d: ожидали hint=true retry=forbidden: %#v", i+1, results[0])
		}
	}
}

// TestReadFilesMixedExistingAndMissing: в одном вызове ReadFiles
// существующий файл читается успешно, а несуществующий — с hint/retry;
// JSON-контракт (массив) не меняется.
func TestReadFilesMixedExistingAndMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	ops := &FileOps{OutputDir: dir}

	raw, err := ops.ReadFiles(map[string]any{"filenames": []string{"real.txt", "missing.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	var results []map[string]string
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatalf("разбор ответа: %v (%s)", err, raw)
	}
	if len(results) != 2 {
		t.Fatalf("ожидали 2 ответа, got %d: %s", len(results), raw)
	}
	if results[0]["status"] != "success" || results[0]["content"] != "hello" {
		t.Errorf("real.txt должен прочитаться: %#v", results[0])
	}
	if results[1]["status"] != "error" || results[1]["hint"] != "true" || results[1]["retry"] != "forbidden" {
		t.Errorf("missing.txt должен быть error с hint/retry: %#v", results[1])
	}
	if !strings.Contains(results[1]["message"], "не существует") {
		t.Errorf("подсказка должна содержать «не существует»: %#v", results[1])
	}
}

// TestReadFilesSetOutputDirResetsCounter: смена OutputDir (смена проекта)
// сбрасывает счётчик попыток — после переключения первая попытка снова
// «первая».
func TestReadFilesSetOutputDirResetsCounter(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	ops := &FileOps{OutputDir: dir1}

	// Две попытки в dir1: счётчик дошёл до 2.
	for i := 0; i < 2; i++ {
		raw, err := ops.ReadFiles(map[string]any{"filenames": []string{"gone.txt"}})
		if err != nil {
			t.Fatal(err)
		}
		var results []map[string]string
		if err := json.Unmarshal(raw, &results); err != nil {
			t.Fatalf("разбор ответа: %v", err)
		}
		if results[0]["retry"] != "forbidden" {
			t.Fatalf("попытка %d в dir1: ожидали retry=forbidden: %#v", i+1, results[0])
		}
	}

	// Смена проекта: счётчик чистый.
	ops.SetOutputDir(dir2)
	raw, err := ops.ReadFiles(map[string]any{"filenames": []string{"gone.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	var results []map[string]string
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}
	if results[0]["retry"] != "forbidden" {
		t.Fatalf("после SetOutputDir: ожидали retry=forbidden: %#v", results[0])
	}
	if strings.Contains(results[0]["message"], "повторная попытка") {
		t.Errorf("после SetOutputDir подсказка не должна помечать повторную попытку: %#v", results[0])
	}
}
