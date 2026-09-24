package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadFilesNoLoopOnMissing — интеграционный тест: симулирует поведение
// модели — 3 последовательных вызова ReadFiles одним и тем же
// несуществующим путём. Каждый вызов должен возвращать status:"error"
// (никогда не «успех» и не паника), с нарастающей подсказкой.
func TestReadFilesNoLoopOnMissing(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	missing := "nope/missing.go"
	for i := 1; i <= 3; i++ {
		out, err := ops.ReadFiles(map[string]any{"filenames": []string{missing}})
		if err != nil {
			t.Fatalf("call %d: ReadFiles error: %v", i, err)
		}
		var res []map[string]string
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("call %d: unmarshal: %v", i, err)
		}
		if len(res) != 1 {
			t.Fatalf("call %d: expected 1 result, got %d: %s", i, len(res), out)
		}
		r := res[0]
		if r["status"] != "error" {
			t.Fatalf("call %d: status = %q, want error (никогда не «успех»)", i, r["status"])
		}
		if r["hint"] != "true" {
			t.Errorf("call %d: hint = %q, want \"true\"", i, r["hint"])
		}
		if r["retry"] != "forbidden" {
			t.Errorf("call %d: retry = %q, want \"forbidden\"", i, r["retry"])
		}
		switch i {
		case 1:
			if !strings.Contains(r["message"], "не существует") {
				t.Errorf("call 1: message %q: не содержит «не существует»", r["message"])
			}
			if !strings.Contains(r["message"], "List") {
				t.Errorf("call 1: message %q: не содержит «List»", r["message"])
			}
			if strings.Contains(r["message"], "повторная попытка") {
				t.Errorf("call 1: message %q: НЕ должна содержать «повторная попытка»", r["message"])
			}
		case 2:
			if !strings.Contains(r["message"], "повторная попытка 2") {
				t.Errorf("call 2: message %q: не содержит «повторная попытка 2»", r["message"])
			}
			if !strings.Contains(r["message"], "запрещены") {
				t.Errorf("call 2: message %q: не содержит «запрещены»", r["message"])
			}
		case 3:
			if !strings.Contains(r["message"], "повторная попытка 3") {
				t.Errorf("call 3: message %q: не содержит «повторная попытка 3»", r["message"])
			}
		}
	}
}

// TestReadFilesMixedExistingAndMissing — вызов с существующим и
// несуществующим файлами: success для existing (content совпадает),
// error+hint для missing; существующий файл читается корректно (регрессия).
func TestReadFilesMixedExistingAndMissing(t *testing.T) {
	dir := t.TempDir()
	existing := "existing.txt"
	content := "hello world\n"
	if err := os.WriteFile(filepath.Join(dir, existing), []byte(content), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	ops := &FileOps{OutputDir: dir}

	out, err := ops.ReadFiles(map[string]any{"filenames": []string{existing, "missing/a.go"}})
	if err != nil {
		t.Fatalf("ReadFiles error: %v", err)
	}
	var res []map[string]string
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d: %s", len(res), out)
	}
	// existing.txt — success, content совпадает
	if res[0]["filename"] != existing {
		t.Errorf("res[0].filename = %q, want %q", res[0]["filename"], existing)
	}
	if res[0]["status"] != "success" {
		t.Errorf("res[0].status = %q, want success", res[0]["status"])
	}
	if res[0]["content"] != content {
		t.Errorf("res[0].content = %q, want %q", res[0]["content"], content)
	}
	// missing/a.go — error + hint
	if res[1]["filename"] != "missing/a.go" {
		t.Errorf("res[1].filename = %q, want missing/a.go", res[1]["filename"])
	}
	if res[1]["status"] != "error" {
		t.Errorf("res[1].status = %q, want error", res[1]["status"])
	}
	if res[1]["hint"] != "true" {
		t.Errorf("res[1].hint = %q, want \"true\"", res[1]["hint"])
	}
	if res[1]["retry"] != "forbidden" {
		t.Errorf("res[1].retry = %q, want \"forbidden\"", res[1]["retry"])
	}
}

// TestReadFilesSetOutputDirResetsCounter — после SetOutputDir на новый
// tempdir счётчик missing-пути обнуляется: 1-я подсказка снова «не
// повторяй вызов» без «повторная попытка».
func TestReadFilesSetOutputDirResetsCounter(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	ops := &FileOps{OutputDir: dir1}

	missing := "nope/missing.go"
	// Два вызова в dir1 — счётчик доходит до 2
	for i := 0; i < 2; i++ {
		out, err := ops.ReadFiles(map[string]any{"filenames": []string{missing}})
		if err != nil {
			t.Fatalf("dir1 call %d: ReadFiles error: %v", i+1, err)
		}
		var res []map[string]string
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("dir1 call %d: unmarshal: %v", i+1, err)
		}
		if len(res) != 1 {
			t.Fatalf("dir1 call %d: expected 1 result, got %d", i+1, len(res))
		}
	}
	// Переключаемся на dir2 — счётчик должен обнулиться
	ops.SetOutputDir(dir2)
	out, err := ops.ReadFiles(map[string]any{"filenames": []string{missing}})
	if err != nil {
		t.Fatalf("dir2 call: ReadFiles error: %v", err)
	}
	var res []map[string]string
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("dir2 call: unmarshal: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("dir2 call: expected 1 result, got %d", len(res))
	}
	r := res[0]
	if r["status"] != "error" {
		t.Fatalf("dir2 call: status = %q, want error", r["status"])
	}
	if !strings.Contains(r["message"], "не существует") {
		t.Errorf("dir2 call: message %q: не содержит «не существует»", r["message"])
	}
	if strings.Contains(r["message"], "повторная попытка") {
		t.Errorf("dir2 call: message %q: НЕ должна содержать «повторная попытка» (счётчик не обнулился)", r["message"])
	}
	if !strings.Contains(r["message"], "Не повторяй вызов") {
		t.Errorf("dir2 call: message %q: не содержит «Не повторяй вызов» (1-я подсказка)", r["message"])
	}
}
