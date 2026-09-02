package gitlab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"ai/forges"
)

// Forge — реализация интерфейса forges.Forge для GitLab.
type Forge struct {
	cfg *config
}

func init() {
	forges.Register(forges.KindGitLab, func(prURL, token string) (forges.Forge, error) {
		return New(prURL, token)
	})
}

// New создаёт GitLab-провайдер по ссылке на Merge Request.
func New(mrURL string, token string) (*Forge, error) {
	cfg, err := ParseURL(mrURL, token)
	if err != nil {
		return nil, err
	}
	return &Forge{cfg: cfg}, nil
}

func (f *Forge) do(method, path string, body any) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(b)
	}

	urlStr := f.cfg.BaseURL + path
	req, err := http.NewRequest(method, urlStr, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("PRIVATE-TOKEN", f.cfg.Token)
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

// GetDiff возвращает изменения Merge Request.
func (f *Forge) GetDiff() (string, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/diffs",
		f.cfg.ProjID, f.cfg.MRIID)

	data, status, err := f.do("GET", path, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("статус %d: %s", status, string(data))
	}
	return string(data), nil
}

// versions возвращает SHA коммитов для позиционирования комментариев.
func (f *Forge) versions() (base, start, head string, err error) {
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s",
		f.cfg.ProjID, f.cfg.MRIID)

	data, status, err := f.do("GET", path, nil)
	if err != nil {
		return "", "", "", err
	}
	if status != http.StatusOK {
		return "", "", "", fmt.Errorf("статус %d: %s", status, string(data))
	}

	var parsed struct {
		DiffRefs struct {
			BaseSha  string `json:"base_sha"`
			StartSha string `json:"start_sha"`
			HeadSha  string `json:"head_sha"`
		} `json:"diff_refs"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", "", "", err
	}
	return parsed.DiffRefs.BaseSha, parsed.DiffRefs.StartSha, parsed.DiffRefs.HeadSha, nil
}

// PostComment публикует замечание. Сначала пробует привязать комментарий к
// строке; если GitLab отвергает привязку, постит комментарий к файлу.
func (f *Forge) PostComment(comment forges.ReviewComment) error {
	if err := f.postCommentOnLine(comment); err != nil {
		return err
	}
	return nil
}

func (f *Forge) postCommentOnLine(comment forges.ReviewComment) error {
	fmt.Println("postCommentOnLine", comment)

	baseSHA, startSHA, headSHA, err := f.versions()
	if err != nil {
		return fmt.Errorf("не удалось получить SHA коммитов: %v", err)
	}

	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/discussions",
		f.cfg.ProjID, f.cfg.MRIID)

	payload := map[string]any{
		"body": comment.Text,
		"position": map[string]any{
			"base_sha":      baseSHA,
			"start_sha":     startSHA,
			"head_sha":      headSHA,
			"position_type": "text",
			"new_path":      comment.FilePath,
			"new_line":      comment.Line,
		},
	}

	data, status, err := f.do("POST", path, payload)
	if err != nil {
		return err
	}

	if status == http.StatusCreated {
		return nil
	}

	// Не удалось привязать к строке — постим комментарий к файлу.
	if status == http.StatusBadRequest && strings.Contains(string(data), "must be a valid line code") {
		return f.postCommentOnFile(comment)
	}

	return fmt.Errorf("статус %d: %s", status, string(data))
}

func (f *Forge) postCommentOnFile(comment forges.ReviewComment) error {
	fmt.Println("postCommentOnFile", comment)

	baseSHA, startSHA, headSHA, err := f.versions()
	if err != nil {
		return fmt.Errorf("не удалось получить SHA коммитов: %v", err)
	}

	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/discussions",
		f.cfg.ProjID, f.cfg.MRIID)

	payload := map[string]any{
		"body": comment.Text,
		"position": map[string]any{
			"base_sha":      baseSHA,
			"start_sha":     startSHA,
			"head_sha":      headSHA,
			"position_type": "file",
			"new_path":      comment.FilePath,
			"old_path":      comment.FilePath,
		},
	}

	data, status, err := f.do("POST", path, payload)
	if err != nil {
		return err
	}

	if status != http.StatusCreated {
		return fmt.Errorf("статус %d: %s", status, string(data))
	}
	return nil
}

// PostSummary публикует итоговый отчёт в общий тред Merge Request
// (заметка на MR).
func (f *Forge) PostSummary(summary string) error {
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/notes",
		f.cfg.ProjID, f.cfg.MRIID)

	payload := map[string]string{"body": summary}

	data, status, err := f.do("POST", path, payload)
	if err != nil {
		return err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return fmt.Errorf("статус %d: %s", status, string(data))
	}
	return nil
}

// Approve одобряет Merge Request. summary — текст легенды (заметка),
// публикуется как комментарий на MR перед одобрением; может быть пустым.
func (f *Forge) Approve(summary string) error {
	// Сначала публикуем легенду ревью, затем одобряем.
	if summary != "" {
		if err := f.PostSummary(summary); err != nil {
			return err
		}
	}

	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/approve",
		f.cfg.ProjID, f.cfg.MRIID)

	data, status, err := f.do("POST", path, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("статус %d: %s", status, string(data))
	}
	return nil
}
