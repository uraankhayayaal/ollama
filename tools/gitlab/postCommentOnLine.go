package gitlab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func PostCommentOnLine(c *GitLabConfig, comment ReviewComment) error {
	fmt.Println("postCommentOnLine", comment)
	urlStr := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/discussions", c.BaseURL, c.ProjID, c.MRIID)

	baseSHA, startSHA, headSHA, err := getMRVersions(c)
	if err != nil {
		return fmt.Errorf("не удалось получить SHA коммитов: %v", err)
	}

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

	jsonValue, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", urlStr, bytes.NewBuffer(jsonValue))
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == 400 && strings.Contains(string(body), "must be a valid line code") {
			err := PostCommentOnFile(c, comment)
			if err != nil {
				return err
			}
			return nil
		}
		return fmt.Errorf("статус %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
