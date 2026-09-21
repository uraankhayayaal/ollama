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
	"strings"

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

// Forge — реализация интерффеса forges.Forge для GitHub.
type Forge struct {
	cfg *config
}

// ParseRemote разбирает ссылку на git-remote (SSH `git@github.com:o/r.git`
// или HTTPS `https://github.com/o/r.git`) и возвращает конфиг без номера
// Pull Request. Используется для создания PR по уже существующей
// подключённой фича-ветке (Ф-2-3): источником служит remote, а не URL PR.
func ParseRemote(remoteURL string, token string) (*config, error) {
	// SSH-вид: git@github.com:owner/repo.git
	if strings.HasPrefix(remoteURL, "git@") {
		scp := strings.TrimPrefix(remoteURL, "git@")
		pieces := strings.Split(scp, ":")
		if len(pieces) != 2 {
			return nil, fmt.Errorf("неверный формат SSH remote: %q", remoteURL)
		}
		host, path := pieces[0], strings.TrimSuffix(pieces[1], ".git")
		parts := strings.Split(path, "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("неверный формат пути в SSH remote: %q", remoteURL)
		}
		return &config{
			BaseURL: "https://api." + host,
			Token:   token,
			Owner:   parts[0],
			Repo:    parts[1],
		}, nil
	}

	// HTTPS-вид: https://github.com/owner/repo.git
	u, err := url.Parse(remoteURL)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSuffix(u.Path, ".git"), "/")
	if len(parts) < 3 || parts[1] == "" || parts[2] == "" {
		return nil, fmt.Errorf("неверный формат HTTPS remote: %q", remoteURL)
	}
	return &config{
		BaseURL: fmt.Sprintf("https://api.%s", u.Hostname()),
		Token:   token,
		Owner:   parts[1],
		Repo:    parts[2],
	}, nil
}

// NewByRemote создаёт GitHub-провайдер по git-remote (без номера PR).
// Используется для HITL-затвора «Принять → PR»: источником создания PR
// служит подключённая фича-ветка.
func NewByRemote(remoteURL string, token string) (*Forge, error) {
	cfg, err := ParseRemote(remoteURL, token)
	if err != nil {
		return nil, err
	}
	return &Forge{cfg: cfg}, nil
}

func init() {
	forges.Register(forges.KindGitHub, func(prURL, token string) (forges.Forge, error) {
		return New(prURL, token)
	})
	forges.RegisterRemote(forges.KindGitHub, func(remoteURL, token string) (forges.Forge, error) {
		return NewByRemote(remoteURL, token)
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

// PostSummary публикует итоговый отчёт в общий тред Pull Request.
func (f *Forge) PostSummary(summary string) error {
	apiPath := fmt.Sprintf("/repos/%s/%s/issues/%s/comments",
		f.cfg.Owner, f.cfg.Repo, f.cfg.PRNumber)

	payload := map[string]string{"body": summary}

	data, status, err := f.do("POST", apiPath, payload)
	if err != nil {
		return err
	}

	if status != http.StatusCreated {
		return fmt.Errorf("статус %d: %s", status, string(data))
	}
	return nil
}

// Approve одобряет Pull Request. summary — текст легенды, прикладываемой
// к апруву (может быть пустым).
func (f *Forge) Approve(summary string) error {
	apiPath := fmt.Sprintf("/repos/%s/%s/pulls/%s/reviews",
		f.cfg.Owner, f.cfg.Repo, f.cfg.PRNumber)

	if summary == "" {
		summary = "Ревью пройдено, изменений принимаю."
	}

	payload := map[string]string{
		"event": "APPROVE",
		"body":  summary,
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

// CreateMergeRequest создаёт Pull Request по параметрам и возвращает ссылку
// на созданный PR (Ф-2-3, HITL-затвор «Принять → MR»). Источником выступает
// фича-ветка, уже запушенная в remote (см. gitops).
//
// ВАЖНО: в отличие от New (который требует живой URL PR), этот метод не
// трогает PR — он создаёт новый, поэтому заголовок/описание задаются явно.
func (f *Forge) CreateMergeRequest(opts forges.MergeRequestOptions) (string, error) {
	// head должен быть "owner:branch" (если ветка из того же репозитория —
	// хватает имени, но GitHub API надёжнее принимает owner:branch).
	payload := map[string]string{
		"title": opts.Title,
		"head":  fmt.Sprintf("%s:%s", f.cfg.Owner, opts.SourceBranch),
		"base":  opts.TargetBranch,
		"body":  opts.Description,
	}

	apiPath := fmt.Sprintf("/repos/%s/%s/pulls", f.cfg.Owner, f.cfg.Repo)
	data, status, err := f.do("POST", apiPath, payload)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return "", fmt.Errorf("статус %d: %s", status, string(data))
	}

	var pr struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(data, &pr); err != nil {
		return "", err
	}
	if pr.HTMLURL == "" {
		return "", fmt.Errorf("GitHub не вернул ссылку на созданный Pull Request")
	}
	return pr.HTMLURL, nil
}

// FindMergeRequest ищет Pull Request с веткой-источником source и целью
// target (Ф-5, сверка MR при открытии дашборда). GitHub умеет фильтровать
// по head (owner:branch) и base. Возвращает первый найденный открытый PR;
// если открытых нет — any-state, чтобы мы могли показать слитый/закрытый.
// Когда подходящего PR нет — forges.ErrNoMergeRequest.
func (f *Forge) FindMergeRequest(source, target string) (*forges.MergeRequestInfo, error) {
	if f.cfg.Owner == "" || f.cfg.Repo == "" {
		return nil, forges.ErrNoMergeRequest
	}
	head := fmt.Sprintf("%s:%s", f.cfg.Owner, source)
	// Сначала открытые PR, потом all-state (вдруг PR слит/закрыт).
	for _, state := range []string{"open", "all"} {
		apiPath := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&head=%s",
			f.cfg.Owner, f.cfg.Repo, state, url.QueryEscape(head))
		if target != "" {
			apiPath += "&base=" + url.QueryEscape(target)
		}
		data, status, err := f.do("GET", apiPath, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			// Не найденная ветка/repo GitHub и так дадут пустой список (200).
			return nil, fmt.Errorf("GitHub: поиск PR по ветке %s: статус %d: %s", source, status, string(data))
		}
		var prs []struct {
			HTMLURL  string  `json:"html_url"`
			State    string  `json:"state"`
			MergedAt *string `json:"merged_at"`
			Head     struct {
				Ref string `json:"ref"`
			} `json:"head"`
		}
		if err := json.Unmarshal(data, &prs); err != nil {
			return nil, err
		}
		for _, pr := range prs {
			if pr.Head.Ref != source {
				continue
			}
			st := pr.State
			if st == "closed" && pr.MergedAt != nil {
				st = "merged"
			}
			return &forges.MergeRequestInfo{URL: pr.HTMLURL, State: st}, nil
		}
	}
	return nil, forges.ErrNoMergeRequest
}
