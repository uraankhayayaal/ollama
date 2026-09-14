package tools

import "testing"

// TestParseDecodedFilesRepairsEscapedQuotes — qwen сериализует массив файлов
// в JSON-строку и двойжды экранирует кавычки внутри содержимого. После
// разбора контент не должен содержать лишних символов \" — иначе Go-код
// не компилируется (illegal character U+005C).
func TestParseDecodedFilesRepairsEscapedQuotes(t *testing.T) {
	raw := `[{"filename":"server/main.go","content":"package main\n\nimport (\"net/http\")\n\nfunc main() { println(\"hi\") }"},{"filename":"server/user.go","content":"package user\n\ntype User struct { Name string \"json:name\" }"}]`
	items := parseDecodedFiles(raw)
	if len(items) != 2 {
		t.Fatalf("ожидалось 2 файла, got %d: %+v", len(items), items)
	}
	mainContent := items[0].Content
	for _, want := range []string{`"net/http"`, `"hi"`, "package main"} {
		if !containsStr(mainContent, want) {
			t.Errorf("контент не содержит %q; got:\n%s", want, mainContent)
		}
	}
	if containsStr(mainContent, `\"`) {
		t.Errorf("в контенте остались битые экранированные кавычки:\n%s", mainContent)
	}
	userContent := items[1].Content
	if !containsStr(userContent, `"json:name"`) {
		t.Errorf("тег структуры повреждён — внутри осталось экранирование:\n%s", userContent)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}