package gitlab

import (
	"fmt"
	"net/url"
	"regexp"
)

// config хранит параметры подключения, извлеченные из URL.
type config struct {
	BaseURL string
	Token   string
	ProjID  string
	MRIID   string
}

// ParseURL разбирает ссылку на Merge Request GitLab.
func ParseURL(mrURL string, token string) (*config, error) {
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

	return &config{
		BaseURL: baseURL,
		Token:   token,
		ProjID:  encodedProjID,
		MRIID:   mrIID,
	}, nil
}
