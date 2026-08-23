package gitlab

import (
	"fmt"
	"io"
	"net/http"
)

func ApproveMR(c *GitLabConfig) error {
	urlStr := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/approve", c.BaseURL, c.ProjID, c.MRIID)
	req, _ := http.NewRequest("POST", urlStr, nil)
	req.Header.Set("PRIVATE-TOKEN", c.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("статус %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
