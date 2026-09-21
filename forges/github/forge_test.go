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

// TestFindMergeRequest проверяет поиск PR по ветке: два запроса (open, all),
// фильтр по head=owner:<branch>&base, статус открытого/слитого PR.
func TestFindMergeRequest(t *testing.T) {
	var hits []string
	prev := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hits = append(hits, r.URL.String())
		body := `[
			{"html_url": "https://github.com/owner/repo/pull/7",
			 "state": "open", "merged_at": null,
			 "head": {"ref": "ai/epic/e1"}},
			{"html_url": "https://github.com/owner/repo/pull/9",
			 "state": "closed", "merged_at": "2026-01-02T00:00:00Z",
			 "head": {"ref": "ai/epic/e1"}}
		]`
		if strings.Contains(r.URL.String(), "state=all") {
			body = `[
				{"html_url": "https://github.com/owner/repo/pull/9",
				 "state": "closed", "merged_at": "2026-01-02T00:00:00Z",
				 "head": {"ref": "ai/epic/e1"}}
			]`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	defer func() { http.DefaultClient = prev }()

	f := &Forge{cfg: &config{
		BaseURL: "https://api.github.com",
		Token:   "tok",
		Owner:   "owner",
		Repo:    "repo",
	}}

	info, err := f.FindMergeRequest("ai/epic/e1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if info.URL != "https://github.com/owner/repo/pull/7" || info.State != "open" {
		t.Errorf("неверный открытый PR: %+v", info)
	}
	if len(hits) < 1 || !strings.Contains(hits[0], "state=open") ||
		!strings.Contains(hits[0], "head=owner%3Aai%2Fepic%2Fe1") {
		t.Errorf("неверный первый запрос search: %v", hits)
	}
}

// TestFindMergeRequestOpenThenMerged проверяет ветку без открытого PR:
// второй запрос (all) находит слитый PR и нормализует state в merged.
func TestFindMergeRequestOpenThenMerged(t *testing.T) {
	prev := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		if strings.Contains(r.URL.String(), "state=all") {
			body = `[
				{"html_url": "https://github.com/owner/repo/pull/5",
				 "state": "closed", "merged_at": "2026-01-02T00:00:00Z",
				 "head": {"ref": "ai/task/t-1"}}
			]`
		} else {
			body = `[]`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	defer func() { http.DefaultClient = prev }()

	f := &Forge{cfg: &config{
		BaseURL: "https://api.github.com",
		Token:   "tok",
		Owner:   "owner",
		Repo:    "repo",
	}}

	info, err := f.FindMergeRequest("ai/task/t-1", "ai/epic/e1")
	if err != nil {
		t.Fatal(err)
	}
	if info.URL != "https://github.com/owner/repo/pull/5" || info.State != "merged" {
		t.Errorf("неверный слитый PR: %+v", info)
	}
}

// TestFindMergeRequestNone проверяет отсутствие PR: оба запроса пусты → ErrNoMergeRequest.
func TestFindMergeRequestNone(t *testing.T) {
	prev := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`[]`)),
		}, nil
	})}
	defer func() { http.DefaultClient = prev }()

	f := &Forge{cfg: &config{
		BaseURL: "https://api.github.com",
		Token:   "tok",
		Owner:   "owner",
		Repo:    "repo",
	}}

	if _, err := f.FindMergeRequest("ai/epic/nope", "main"); err != forges.ErrNoMergeRequest {
		t.Errorf("ожидали ErrNoMergeRequest, получили: %v", err)
	}
}
