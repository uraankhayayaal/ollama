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
	// Кэши результатов чтения MR: за время одного ревью дифф и SHA коммитов
	// не меняются, а каждый комментарий не должен порождать лишние GET.
	// Мемоизация только успеха — ошибки не запоминаются, чтобы сбой сети
	// не «залипал» на всё ревью.
	diffDone   bool
	diffResult string
	diffErr    error

	versionsDone bool
	verBase      string
	verStart     string
	verHead      string
	verErr       error
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

// diffsPageSize — сколько файлов диффа запрашивается за одну страницу.
const diffsPageSize = 100

// diffFile — файл в ответе GitLab API /merge_requests/:iid/diffs.
type diffFile struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Diff    string `json:"diff"`
}

// fetchDiffs загружает все страницы диффа Merge Request.
func (f *Forge) fetchDiffs() ([]diffFile, error) {
	var files []diffFile
	for page := 1; ; page++ {
		path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s/diffs?per_page=%d&page=%d",
			f.cfg.ProjID, f.cfg.MRIID, diffsPageSize, page)

		data, status, err := f.do("GET", path, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("статус %d: %s", status, string(data))
		}

		var pageFiles []diffFile
		if err := json.Unmarshal(data, &pageFiles); err != nil {
			return nil, err
		}
		files = append(files, pageFiles...)

		// Результатов меньше размера страницы — это последняя страница.
		if len(pageFiles) < diffsPageSize {
			return files, nil
		}
	}
}

// GetDiff возвращает изменения Merge Request единым unified-диффом.
// GitLab API отдаёт массив объектов {old_path, new_path, diff}, где diff —
// только ханки без заголовков ---/+++. Собираем их в обычный формат
// "diff --git a/.. b/..\n--- a/..\n+++ b/..\n<ханки>", который ожидают
// фильтр сгенерированных файлов и валидация комментариев. Результат
// кэшируется: повторные вызовы не обращаются к API.
func (f *Forge) GetDiff() (string, error) {
	if f.diffDone {
		return f.diffResult, f.diffErr
	}

	files, err := f.fetchDiffs()

	var out strings.Builder
	for _, file := range files {
		if file.NewPath == "" {
			continue
		}
		out.WriteString("diff --git a/" + file.OldPath + " b/" + file.NewPath + "\n")
		out.WriteString("--- a/" + file.OldPath + "\n")
		out.WriteString("+++ b/" + file.NewPath + "\n")
		out.WriteString(file.Diff)
		if !strings.HasSuffix(file.Diff, "\n") {
			out.WriteByte('\n')
		}
	}

	f.diffDone = true
	f.diffResult = out.String()
	f.diffErr = err
	return f.diffResult, f.diffErr
}

// versions возвращает SHA коммитов для позиционирования комментариев.
// Результат кэшируется на структуре.
func (f *Forge) versions() (base, start, head string, err error) {
	if f.versionsDone {
		return f.verBase, f.verStart, f.verHead, f.verErr
	}

	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%s",
		f.cfg.ProjID, f.cfg.MRIID)

	data, status, err := f.do("GET", path, nil)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("статус %d: %s", status, string(data))
	}

	var parsed struct {
		DiffRefs struct {
			BaseSha  string `json:"base_sha"`
			StartSha string `json:"start_sha"`
			HeadSha  string `json:"head_sha"`
		} `json:"diff_refs"`
	}
	if err == nil {
		err = json.Unmarshal(data, &parsed)
	}

	f.versionsDone = true
	f.verBase, f.verStart, f.verHead = parsed.DiffRefs.BaseSha, parsed.DiffRefs.StartSha, parsed.DiffRefs.HeadSha
	f.verErr = err
	return f.verBase, f.verStart, f.verHead, f.verErr
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
