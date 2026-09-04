package gitlab

import (
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
}
