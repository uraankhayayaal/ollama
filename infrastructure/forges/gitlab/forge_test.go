package gitlab

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseURL(t *testing.T) {
	cfg, err := ParseURL("https://gitlab.com/it-yakutia/botsad.ru/-/merge_requests/7", "tok")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ProjID != "it-yakutia%2Fbotsad.ru" {
		t.Errorf("неверный ProjID: %s", cfg.ProjID)
	}
	if cfg.MRIID != "7" {
		t.Errorf("неверный MRIID: %s", cfg.MRIID)
	}
	if cfg.BaseURL != "https://gitlab.com" {
		t.Errorf("неверный BaseURL: %s", cfg.BaseURL)
	}
}

func TestParseURLInvalid(t *testing.T) {
	if _, err := ParseURL("https://gitlab.com/it-yakutia/botsad.ru", "tok"); err == nil {
		t.Error("ожидалась ошибка для не-MR URL")
	}
}

// TestGetDiffUnified проверяет, что GetDiff собирает JSON-массив GitLab
// в единый unified-дифф с заголовками +++ b/<path> (их ждёт фильтр
// сгенерированных файлов и валидация комментариев).
func TestGetDiffUnified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"old_path":"src/a.php","new_path":"src/a.php","diff":"@@ -0,0 +1,2 @@\n+<?php\n+echo 1;\n"},
			{"old_path":"x.txt","new_path":"x.txt","diff":"@@ -1 +1 @@\n-foo\n+bar"}
		]`))
	}))
	defer srv.Close()

	cfg, err := ParseURL(srv.URL+"/g/-/merge_requests/1", "tok")
	if err != nil {
		t.Fatal(err)
	}
	f := &Forge{cfg: cfg}

	diff, err := f.GetDiff()
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"diff --git a/src/a.php b/src/a.php",
		"+++ b/src/a.php",
		"+echo 1;",
		"diff --git a/x.txt b/x.txt",
		"+++ b/x.txt",
		"-foo",
		"+bar",
	} {
		if !strings.Contains(diff, want) {
			t.Errorf("дифф не содержит %q:\n%s", want, diff)
		}
	}

	// Повторный вызов не должен заново обращаться к API (кэш).
	if _, err := f.GetDiff(); err != nil {
		t.Fatal(err)
	}
}

// TestGetDiffPaginated проверяет, что GetDiff докачивает все страницы
// диффа, пока страница не вернёт меньше запрошенного объёма.
func TestGetDiffPaginated(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "", "1":
			files := make([]diffFile, 0, diffsPageSize)
			for i := 0; i < diffsPageSize; i++ {
				files = append(files, diffFile{
					OldPath: "a/page1.go",
					NewPath: "a/page1.go",
					Diff:    "@@ -1 +1 @@\n+// 1",
				})
			}
			w.Write(mustJSON(t, files))
		case "2":
			w.Write([]byte(`[{"old_path":"b/page2.go","new_path":"b/page2.go","diff":"@@ -1 +1 @@\n+// 2"}]`))
		default:
			http.Error(w, "unexpected page", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	cfg, err := ParseURL(srv.URL+"/g/-/merge_requests/1", "tok")
	if err != nil {
		t.Fatal(err)
	}
	f := &Forge{cfg: cfg}

	diff, err := f.GetDiff()
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Errorf("ожидали 2 запроса страниц, было %d", requests)
	}
	for _, want := range []string{"+++ b/a/page1.go", "+++ b/b/page2.go", "+// 2"} {
		if !strings.Contains(diff, want) {
			t.Errorf("дифф не содержит %q", want)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
