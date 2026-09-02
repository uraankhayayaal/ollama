package github

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai/forges"
)

func TestPostCommentFallbackToPRThread(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls/42/comments") {
			// Строка вне диффа → GitHub отвечает 422.
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"message":"line out of diff"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/issues/42/comments") {
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":1}`))
			return
		}
		t.Errorf("неожиданный запрос: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	f := &Forge{cfg: &config{
		BaseURL: server.URL, Owner: "o", Repo: "r", PRNumber: "42",
	}}

	err := f.PostComment(forges.ReviewComment{
		FilePath: "src/app/TodoController.php",
		Line:     10,
		Text:     "замечание",
	})
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
}

func TestResolveHeadCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls/42") {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"head":{"sha":"abc123"}}`)
			return
		}
		t.Errorf("неожиданный запрос: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	f := &Forge{cfg: &config{
		BaseURL: server.URL, Owner: "o", Repo: "r", PRNumber: "42",
	}}
	if err := f.resolveHeadCommit(); err != nil {
		t.Fatalf("resolveHeadCommit: %v", err)
	}
	if f.cfg.CommitID != "abc123" {
		t.Errorf("CommitID = %q, want abc123", f.cfg.CommitID)
	}
}