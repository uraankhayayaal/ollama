package gitlab

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
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

// ParseRemote разбирает git-remote GitLab (SSH `git@host:group/proj.git`
// или HTTPS `https://host/group/proj.git`, путь может включать подгруппы:
// `git@host:g1/g2/proj.git`). Номера MR ещё нет — это конструктор для
// HITL-затвора «Принять → MR» (Ф-2-3): фича-ветка подготовлена, MR будет
// создан позже через CreateMergeRequest.
func ParseRemote(remoteURL string, token string) (*config, error) {
	projectPath := ""

	// SSH-вид `git@gitlab.com:group/proj.git`.
	if strings.HasPrefix(remoteURL, "git@") {
		scp := strings.TrimPrefix(remoteURL, "git@")
		pieces := strings.SplitN(scp, ":", 2)
		if len(pieces) != 2 {
			return nil, fmt.Errorf("неверный формат SSH remote GitLab: %q", remoteURL)
		}
		projectPath = strings.TrimSuffix(pieces[1], ".git")
	}

	if projectPath == "" {
		// HTTPS-вид `https://gitlab.com/group/proj.git`.
		u, err := url.Parse(remoteURL)
		if err != nil {
			return nil, err
		}
		projectPath = strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	}
	if projectPath == "" {
		return nil, fmt.Errorf("не удалось извлечь путь проекта GitLab из remote: %q", remoteURL)
	}

	host := remoteHost(remoteURL)
	return &config{
		BaseURL: "https://" + host,
		Token:   token,
		ProjID:  url.QueryEscape(projectPath),
		MRIID:   "",
	}, nil
}

// remoteHost возвращает хост git-remote (host GitLab, не API).
func remoteHost(remoteURL string) string {
	if strings.HasPrefix(remoteURL, "git@") {
		scp := strings.TrimPrefix(remoteURL, "git@")
		if i := strings.IndexByte(scp, ':'); i > 0 {
			return scp[:i]
		}
	}
	last := strings.LastIndex(remoteURL, "://")
	rest := remoteURL
	if last >= 0 {
		rest = remoteURL[last+3:]
	}
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return rest[:i]
	}
	return rest
}
