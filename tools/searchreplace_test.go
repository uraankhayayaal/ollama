package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplySearchReplaceFirstOccurrence(t *testing.T) {
	content := "a\nb\na\n"
	got, err := ApplySearchReplace(content, []SearchReplacePatch{
		{Search: "a", Replace: "A"},
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	if got != "A\nb\na\n" {
		t.Errorf("должно замениться только первое вхождение, got: %q", got)
	}
}

func TestApplySearchReplaceMissingBlockErrors(t *testing.T) {
	content := "const a = 1;\n"
	_, err := ApplySearchReplace(content, []SearchReplacePatch{
		{Search: "const missing = 2;", Replace: "const x = 3;"},
	})
	if err == nil || !strings.Contains(err.Error(), "не найден") {
		t.Fatalf("ожидалась ошибка о ненайденном фрагменте, got: %v", err)
	}
}

func TestApplySearchReplaceEmptySearchRejected(t *testing.T) {
	_, err := ApplySearchReplace("abc", []SearchReplacePatch{{Search: "", Replace: "x"}})
	if err == nil {
		t.Fatal("пустой search должен быть отклонён")
	}
}

func TestApplySearchReplaceSequential(t *testing.T) {
	content := "foo\n"
	got, err := ApplySearchReplace(content, []SearchReplacePatch{
		{Search: "foo", Replace: "bar"},
		{Search: "bar", Replace: "baz"},
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	if got != "baz\n" {
		t.Errorf("патчи применяются последовательно, got: %q", got)
	}
}

func TestApplySearchReplaceCRLFNormalized(t *testing.T) {
	content := "const a = 1;\r\nconst b = 2;\r\n"
	got, err := ApplySearchReplace(content, []SearchReplacePatch{
		{Search: "const b = 2;", Replace: "const b = 20;"},
	})
	if err != nil {
		t.Fatalf("ApplySearchReplace: %v", err)
	}
	if !strings.Contains(got, "const b = 20;") || strings.Contains(got, "\r") {
		t.Errorf("CRLF не нормализован, got: %q", got)
	}
}

func TestParseSearchReplaceBlocks(t *testing.T) {
	raw := `<<<<<<< SEARCH
const [user, setUser] = useState<User | null>(null);
=======
const [user, setUser] = useState<User | null>(null);
const [isLoading, setIsLoading] = useState<boolean>(false);
>>>>>>> REPLACE

<<<<<<< SEARCH
foo
=======
bar
>>>>>>> REPLACE`
	patches := parseSearchReplaceBlocks(raw)
	if len(patches) != 2 {
		t.Fatalf("ожидалось 2 блока, got %d", len(patches))
	}
	if !strings.Contains(patches[0].Search, "useState<User") ||
		!strings.Contains(patches[0].Replace, "isLoading") {
		t.Errorf("блок 1 разобран неверно: %+v", patches[0])
	}
	if patches[1].Search != "foo" || patches[1].Replace != "bar" {
		t.Errorf("блок 2 разобран неверно: %+v", patches[1])
	}
}

// parseSearchReplaceFiles должен понимать все формы поля files: массив
// объектов с patches, а также файлы с «сырым» текстом блоков в content.
func TestParseSearchReplaceFilesTolerant(t *testing.T) {
	items := parseSearchReplaceFiles([]map[string]any{
		{"filename": "a.ts", "patches": []map[string]any{{"search": "x", "replace": "y"}}},
		{"filename": "b.go", "content": "<<<<<<< SEARCH\nfoo\n=======\nbar\n>>>>>>> REPLACE"},
	})
	if len(items) != 2 {
		t.Fatalf("ожидалось 2 файла, got %d", len(items))
	}
	if len(items[0].Patches) != 1 || items[0].Patches[0].Search != "x" {
		t.Errorf("файл a.ts: %+v", items[0])
	}
	if len(items[1].Patches) != 1 || items[1].Patches[0].Search != "foo" || items[1].Patches[0].Replace != "bar" {
		t.Errorf("файл b.go: content не разобран в blocks, %+v", items[1])
	}

	// JSON-строка как форма передачи.
	strItems := parseSearchReplaceFiles(`[{"filename":"c.ts","patches":[{"search":"a","replace":"b"}]}]`)
	if len(strItems) != 1 || len(strItems[0].Patches) != 1 {
		t.Errorf("JSON-строка не разобрана: %+v", strItems)
	}
}

func TestFileOpsSearchReplaceHandler(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	if err := os.WriteFile(filepath.Join(dir, "App.tsx"), []byte("const [user, setUser] = useState<User | null>(null);\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := ops.SearchReplace(map[string]any{"files": []map[string]any{
		{
			"filename": "App.tsx",
			"patches": []map[string]any{
				{"search": "const [user, setUser] = useState<User | null>(null);",
					"replace": "const [user, setUser] = useState<User | null>(null);\nconst [isLoading, setIsLoading] = useState<boolean>(false);"},
			},
		},
	}})
	if err != nil {
		t.Fatalf("SearchReplace: %v", err)
	}
	if !strings.Contains(string(out), `"status":"success"`) {
		t.Fatalf("ожидался успех, got: %s", out)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "App.tsx"))
	if !strings.Contains(string(got), "isLoading") {
		t.Errorf("патч не применён:\n%s", got)
	}

	// Неверный SEARCH — файл не должен измениться.
	before, _ := os.ReadFile(filepath.Join(dir, "App.tsx"))
	out, err = ops.SearchReplace(map[string]any{"files": []map[string]any{
		{"filename": "App.tsx", "patches": []map[string]any{
			{"search": "const noSuch = true;", "replace": "const evil = true;"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"status":"error"`) {
		t.Fatalf("ожидался error, got: %s", out)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "App.tsx"))
	if string(before) != string(after) {
		t.Error("файл изменился при неудачном SEARCH — атомарность нарушена")
	}
}
