package gitlab

import (
	"fmt"
	"io"
	"net/http"
)

// === Реализация API-инструментов GitLab ===
func GetMRDiff(c *GitLabConfig) (string, error) {
	urlStr := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/diffs", c.BaseURL, c.ProjID, c.MRIID)
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("PRIVATE-TOKEN", c.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("статус %d: %s", resp.StatusCode, string(body))
	}

	return string(body), nil
}
