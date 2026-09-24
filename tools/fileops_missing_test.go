package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadFilesMissingFileHint — unit-тест из BUG-01-T1: первый вызов
// ReadFiles на несуществующий путь возвращает error с подсказкой, hint и
// retry=forbidden.
func TestReadFilesMissingFileHint(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	out, err := ops.ReadFiles(map[string]any{"filenames": []string{"nope/missing.go"}})
	if err != nil {
		t.Fatalf("ReadFiles error: %v", err)
	}
	var res []map[string]string
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d: %s", len(res), out)
	}
	r := res[0]
	if r["status"] != "error" {
		t.Errorf("status = %q, want error", r["status"])
	}
	if !strings.Contains(r["message"], "не существует") {
		t.Errorf("message %q: не содержит «не существует»", r["message"])
	}
	if !strings.Contains(r["message"], "List") {
		t.Errorf("message %q: не содержит «List»", r["message"])
	}
	if r["hint"] != "true" {
		t.Errorf("hint = %q, want \"true\"", r["hint"])
	}
	if r["retry"] != "forbidden" {
		t.Errorf("retry = %q, want \"forbidden\"", r["retry"])
	}
}

// TestReadFilesMissingFileRepeatLimit — повторный вызов тем же путём
// усиливает подсказку до запрета повторных попыток.
func TestReadFilesMissingFileRepeatLimit(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}

	for i := 0; i < 2; i++ {
		out, err := ops.ReadFiles(map[string]any{"filenames": []string{"nope/missing.go"}})
		if err != nil {
			t.Fatalf("ReadFiles call %d error: %v", i+1, err)
		}
		var res []map[string]string
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(res) != 1 {
			t.Fatalf("call %d: expected 1 result, got %d", i+1, len(res))
		}
		if i == 1 {
			if !strings.Contains(res[0]["message"], "повторная попытка 2") {
				t.Errorf("call 2: message %q: не содержит «повторная попытка 2»", res[0]["message"])
			}
			if !strings.Contains(res[0]["message"], "запрещены") {
				t.Errorf("call 2: message %q: не содержит «запрещены»", res[0]["message"])
			}
		}
	}
}

// TestReadFilesMissingDir — путь на существующую директорию возвращает
// error с сообщением «директория».
func TestReadFilesMissingDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "somedir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ops := &FileOps{OutputDir: dir}

	out, err := ops.ReadFiles(map[string]any{"filenames": []string{"somedir"}})
	if err != nil {
		t.Fatalf("ReadFiles error: %v", err)
	}
	var res []map[string]string
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 result, got %d", len(res))
	}
	if res[0]["status"] != "error" {
		t.Errorf("status = %q, want error", res[0]["status"])
	}
	if !strings.Contains(res[0]["message"], "директория") {
		t.Errorf("message %q: не содержит «директория»", res[0]["message"])
	}
}
