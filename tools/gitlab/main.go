package gitlab

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
)

// Структура для элемента массива
type ReviewComment struct {
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
	Text     string `json:"text"`
}

// Структура, описывающая ВСЕ аргументы функции post_comments
type PostCommentsArgs struct {
	Comments []ReviewComment `json:"comments"`
}

// GitLabConfig хранит параметры подключения, извлеченные из URL
type GitLabConfig struct {
	BaseURL string
	Token   string
	ProjID  string
	MRIID   string
}

func getMRVersions(c *GitLabConfig) (string, string, string, error) {
	urlStr := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s", c.BaseURL, c.ProjID, c.MRIID)
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("PRIVATE-TOKEN", c.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	var data struct {
		DiffRefs struct {
			BaseSha  string `json:"base_sha"`
			StartSha string `json:"start_sha"`
			HeadSha  string `json:"head_sha"`
		} `json:"diff_refs"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", "", err
	}
	return data.DiffRefs.BaseSha, data.DiffRefs.StartSha, data.DiffRefs.HeadSha, nil
}

func ParseGitLabURL(mrURL string, token string) (*GitLabConfig, error) {
	u, err := url.Parse(mrURL)
	if err != nil {
		return nil, err
	}

	baseURL := fmt.Sprintf("%s://%s", u.Scheme, u.Host)

	re := regexp.MustCompile(`/(.+)/-/merge_requests/(\d+)`)
	matches := re.FindStringSubmatch(u.Path)
	if len(matches) < 3 {
		return nil, fmt.Errorf("неверный формат URL GitLab Merge Request")
	}

	projectPath := matches[1]
	mrIID := matches[2]
	encodedProjID := url.QueryEscape(projectPath)

	return &GitLabConfig{
		BaseURL: baseURL,
		Token:   token,
		ProjID:  encodedProjID,
		MRIID:   mrIID,
	}, nil
}
