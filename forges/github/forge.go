package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"

	"ai/forges"
)

// config хранит параметры подключения, извлеченные из URL Pull/Merge Request.
type config struct {
	BaseURL  string
	Token    string
	Owner    string
	Repo     string
	PRNumber string
}

// ParseURL разбирает ссылку на Pull Request GitHub.
func ParseURL(prURL string, token string) (*config, error) {
	u, err := url.Parse(prURL)
	if err != nil {
		return nil, err
	}

	baseURL := fmt.Sprintf("%s://%s", u.Scheme, u.Host)

	re := regexp.MustCompile(`^/([^/]+)/([^/]+)/pull/(\d+)`)
	matches := re.FindStringSubmatch(u.Path)
	if len(matches) < 4 {
		return nil, fmt.Errorf("неверный формат URL GitHub Pull Request")
	}

	return &config{
		BaseURL:  baseURL,
		Token:    token,
		Owner:    matches[1],
		Repo:     matches[2],
		PRNumber: matches[3],
	}, nil
}

// Forge — реализация интерфейса forges.Forge для GitHub.
type Forge struct {
	cfg *config
}

func init() {
	forges.Register(forges.KindGitHub, func(prURL, token string) (forges.Forge, error) {
		return New(prURL, token)
	})
}

// New создаёт GitHub-провайдер по ссылке на Pull Request.
func New(prURL string, token string) (*Forge, error) {
	cfg, err := ParseURL(prURL, token)
	if err != nil {
		return nil, err
	}
	return &Forge{cfg: cfg}, nil
}

func (f *Forge) do(method, apiPath string, body any) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(b)
	}

	urlStr := f.cfg.BaseURL + apiPath
	req, err := http.NewRequest(method, urlStr, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+f.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	return data, resp.StatusCode, nil
}

// GetDiff возвращает изменения Pull Request.
func (f *Forge) GetDiff() (string, error) {
	apiPath := fmt.Sprintf("/repos/%s/%s/pulls/%s",
		f.cfg.Owner, f.cfg.Repo, f.cfg.PRNumber)

	req, err := http.NewRequest("GET", f.cfg.BaseURL+apiPath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+f.cfg.Token)
	req.Header.Set("Accept", "application/vnd.github.v3.diff")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("статус %d: %s", resp.StatusCode, string(data))
	}
	return string(data), nil
}

// PostComment публикует замечание на строку Pull Request.
func (f *Forge) PostComment(comment forges.ReviewComment) error {
	apiPath := fmt.Sprintf("/repos/%s/%s/pulls/%s/comments",
		f.cfg.Owner, f.cfg.Repo, f.cfg.PRNumber)

	payload := map[string]any{
		"path":     comment.FilePath,
		"line":     comment.Line,
		"side":     "RIGHT",
		"body":     comment.Text,
		"position": comment.Line,
	}

	data, status, err := f.do("POST", apiPath, payload)
	if err != nil {
		return err
	}

	if status != http.StatusCreated {
		return fmt.Errorf("статус %d: %s", status, string(data))
	}
	return nil
}

// Approve одобряет Pull Request.
func (f *Forge) Approve() error {
	apiPath := fmt.Sprintf("/repos/%s/%s/pulls/%s/reviews",
		f.cfg.Owner, f.cfg.Repo, f.cfg.PRNumber)

	payload := map[string]string{
		"event": "APPROVE",
		"body":  "Ревью пройдено, изменений принимаю.",
	}

	data, status, err := f.do("POST", apiPath, payload)
	if err != nil {
		return err
	}

	if status != http.StatusCreated && status != http.StatusOK {
		return fmt.Errorf("статус %d: %s", status, string(data))
	}
	return nil
}