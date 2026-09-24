package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decodeReadResult(t *testing.T, raw []byte) []map[string]string {
	t.Helper()
	var result []map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("ответ не JSON-массив объектов: %v", err)
	}
	return result
}

func TestReadFilesMissingFileHint(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	raw, err := ops.ReadFiles(map[string]any{"filenames": []string{"nope/missing.go"}})
	if err != nil {
		t.Fatalf("ReadFiles: %v", err)
	}
	result := decodeReadResult(t, raw)
	if len(result) != 1 {
		t.Fatalf("ожидался 1 элемент, получено %d", len(result))
	}
	r := result[0]
	if r["status"] != "error" {
		t.Errorf("ожидался status=error, получено %q", r["status"])
	}
	if !strings.Contains(r["message"], "не существует") {
		t.Errorf("message не содержит «не существует»: %q", r["message"])
	}
	if !strings.Contains(r["message"], "List") {
		t.Errorf("message не содержит «List»: %q", r["message"])
	}
	if r["hint"] != "true" {
		t.Errorf("ожидался hint=true, получено %q", r["hint"])
	}
	if r["retry"] != "forbidden" {
		t.Errorf("ожидался retry=forbidden, получено %q", r["retry"])
	}
}

func TestReadFilesMissingFileRepeatLimit(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	// Первый вызов: базовая подсказка.
	if raw, err := ops.ReadFiles(map[string]any{"filenames": []string{"nope/missing.go"}}); err != nil {
		t.Fatalf("ReadFiles (первый): %v", err)
	} else {
		result := decodeReadResult(t, raw)
		if len(result) != 1 {
			t.Fatalf("ожидался 1 элемент, получено %d", len(result))
		}
	}

	// Повторные вызовы тем же путём: счётчик превысил readMaxMissingAttempts —
	// подсказка усиливается запретом повторных вызовов.
	for i := 0; i < readMaxMissingAttempts; i++ {
		if _, err := ops.ReadFiles(map[string]any{"filenames": []string{"nope/missing.go"}}); err != nil {
			t.Fatalf("ReadFiles (вызов %d): %v", i+1, err)
		}
	}
	raw, err := ops.ReadFiles(map[string]any{"filenames": []string{"nope/missing.go"}})
	if err != nil {
		t.Fatalf("ReadFiles (повтор): %v", err)
	}
	result := decodeReadResult(t, raw)
	r := result[0]
	if !strings.Contains(r["message"], "повторная попытка") {
		t.Errorf("message не содержит «повторная попытка»: %q", r["message"])
	}
	if !strings.Contains(r["message"], "запрещены") {
		t.Errorf("message не содержит «запрещены»: %q", r["message"])
	}
	if r["retry"] != "forbidden" {
		t.Errorf("ожидался retry=forbidden, получено %q", r["retry"])
	}
}

func TestReadFilesMissingDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "somedir"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	ops := &FileOps{OutputDir: dir}

	raw, err := ops.ReadFiles(map[string]any{"filenames": []string{"somedir"}})
	if err != nil {
		t.Fatalf("ReadFiles: %v", err)
	}
	result := decodeReadResult(t, raw)
	if len(result) != 1 {
		t.Fatalf("ожидался 1 элемент, получено %d", len(result))
	}
	r := result[0]
	if r["status"] != "error" {
		t.Errorf("ожидался status=error, получено %q", r["status"])
	}
	if !strings.Contains(r["message"], "директория") {
		t.Errorf("message не содержит «директория»: %q", r["message"])
	}
}
