package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestReadFilesRespectsLimits(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 500)
	files := map[string]string{
		"big.txt":   big,
		"small.txt": "hello",
		"late.txt":  "later",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ops := &FileOps{OutputDir: dir}
	t.Setenv("CODEGEN_READ_MAX_FILE", "50")

	// Сначала измеряем фактический размер big.txt после обрезки (содержит
	// пометку фиксированной длины), затем подбираем суммарный лимит так,
	// чтобы поместился ещё один маленький файл, а второй — нет.
	raw, err := ops.ReadFiles(map[string]any{"filenames": []string{"big.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	var first []map[string]string
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}
	bigLen := len(first[0]["content"])
	if bigLen <= 50 || !strings.Contains(first[0]["content"], "содержание обрезано") {
		t.Fatalf("big.txt должен быть обрезан с пометкой (len=%d): %#v", bigLen, first[0])
	}

	// Между bigLen и bigLen+5+5 = второй маленький файл уже не влезает.
	t.Setenv("CODEGEN_READ_MAX_TOTAL", strconv.Itoa(bigLen+5))

	raw2, err := ops.ReadFiles(map[string]any{
		"filenames": []string{"big.txt", "small.txt", "late.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var results []map[string]string
	if err := json.Unmarshal(raw2, &results); err != nil {
		t.Fatalf("разбор ответа: %v (%s)", err, raw2)
	}

	if len(results) != 3 {
		t.Fatalf("ожидали 3 ответа, got %d: %s", len(results), raw2)
	}
	// big.txt обрезан, small.txt поместился, late.txt пропущен суммарным лимитом.
	if results[0]["status"] != "success" || !strings.Contains(results[0]["content"], "содержание обрезано") {
		t.Errorf("big.txt должен быть обрезан: %#v", results[0])
	}
	if results[1]["status"] != "success" || results[1]["content"] != "hello" {
		t.Errorf("small.txt должен прочитаться целиком: %#v", results[1])
	}
	if results[2]["status"] != "skipped" {
		t.Errorf("late.txt должен быть пропущен суммарным лимитом: %#v", results[2])
	}
}
