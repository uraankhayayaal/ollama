package github

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"ai/forges"
)

func TestParseURL(t *testing.T) {
	cfg, err := ParseURL("https://github.com/owner/repo/pull/42", "tok")
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Owner != "owner" || cfg.Repo != "repo" || cfg.PRNumber != "42" {
		t.Errorf("неверный разбор: %+v", cfg)
	}
	if cfg.BaseURL != "https://api.github.com" {
		t.Errorf("неверный BaseURL: %s", cfg.BaseURL)
	}
}

func TestParseURLInvalid(t *testing.T) {
	if _, err := ParseURL("https://github.com/owner/repo", "tok"); err == nil {
		t.Error("ожидалась ошибка для не-PR URL")
	}
}

// roundTripFunc позволяет подменить http.DefaultClient на трамбовочную трубку
// (hermetic): тест проверяет отправленное тело запроса и подставляет ответ
// GitHub API без реальной сети.
type roundTripFunc func(r *http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

// TestCreateMergeRequest проверяет создание Pull Request через POST /pulls:
// head = "owner:branch", base = targetBranch, а возврат — html_url из JSON.
func TestCreateMergeRequest(t *testing.T) {
	var gotBody []byte

	prev := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		gotBody = b
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"html_url": "https://github.com/owner/repo/pull/999"
			}`)),
		}, nil
	})}
	defer func() { http.DefaultClient = prev }()

	f := &Forge{cfg: &config{
		BaseURL: "https://api.github.com",
		Token:   "tok",
		Owner:   "owner",
		Repo:    "repo",
	}}

	htmlURL, err := f.CreateMergeRequest(forges.MergeRequestOptions{
		SourceBranch: "feature-1",
		TargetBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if htmlURL != "https://github.com/owner/repo/pull/999" {
		t.Errorf("неверная ссылка на созданный PR: %s", htmlURL)
	}

	var payload struct {
		Head string `json:"head"`
		Base string `json:"base"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Head != "owner:feature-1" {
		t.Errorf("неверный head: %s", payload.Head)
	}
	if payload.Base != "main" {
		t.Errorf("неверный base: %s", payload.Base)
	}
}
