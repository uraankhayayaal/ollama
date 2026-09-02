package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"ai/forges"
)

// config хранит параметры подключения, извлеченные из URL Pull/Merge Request.
type config struct {
	BaseURL  string
	Token    string
	Owner    string
	Repo     string
	PRNumber string
	// CommitID — SHA головного коммита PR. Требуется GitHub API для
	// inline review comments (commit_id).
	CommitID string
}

// ParseURL разбирает ссылку на Pull Request GitHub.
func ParseURL(prURL string, token string) (*config, error) {
	u, err := url.Parse(prURL)
	if err != nil {
		return nil, err
	}

	// GitHub REST API живёт на api.<host>, а не на веб-хосте github.com.
	apiHost := "api." + u.Hostname()
	baseURL := fmt.Sprintf("%s://%s", u.Scheme, apiHost)

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

	f := &Forge{cfg: cfg}
	if err := f.resolveHeadCommit(); err != nil {
		return nil, err
	}
	return f, nil
}

// resolveHeadCommit получает SHA головного коммита PR и сохраняет его в конфиге.
func (f *Forge) resolveHeadCommit() error {
	apiPath := fmt.Sprintf("/repos/%s/%s/pulls/%s",
		f.cfg.Owner, f.cfg.Repo, f.cfg.PRNumber)

	data, status, err := f.do("GET", apiPath, nil)
	if err != nil {
		return err
	}

	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(data, &pr); err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("статус %d: %s", status, string(data))
	}
	if pr.Head.SHA == "" {
		return fmt.Errorf("не удалось получить SHA головного коммита PR")
	}

	f.cfg.CommitID = pr.Head.SHA
	return nil
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
// Если привязка к строке невозможна (строка вне диффа), публикует
// замечание в общий тред PR, чтобы оно гарантированно отобразилось.
func (f *Forge) PostComment(comment forges.ReviewComment) error {
	apiPath := fmt.Sprintf("/repos/%s/%s/pulls/%s/comments",
		f.cfg.Owner, f.cfg.Repo, f.cfg.PRNumber)

	payload := map[string]any{
		"path":      comment.FilePath,
		"line":      comment.Line,
		"side":      "RIGHT",
		"body":      comment.Text,
		"commit_id": f.cfg.CommitID,
	}

	_, status, err := f.do("POST", apiPath, payload)
	if err != nil {
		return err
	}

	// GitHub отклоняет строку, не входящую в дифф (422). Тогда комментируем
	// в общий тред PR, чтобы замечание не терялось.
	if status != http.StatusCreated {
		return f.postToPRThread(comment)
	}
	return nil
}

// postToPRThread публикует замечание в общий комментарий к Pull Request.
func (f *Forge) postToPRThread(comment forges.ReviewComment) error {
	apiPath := fmt.Sprintf("/repos/%s/%s/issues/%s/comments",
		f.cfg.Owner, f.cfg.Repo, f.cfg.PRNumber)

	payload := map[string]string{
		"body": comment.FilePath + " (строка " + strconv.Itoa(comment.Line) + "): " + comment.Text,
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
