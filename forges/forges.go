package forges

import (
	"net/url"
	"strings"
)

// ReviewComment — замечание к строке кода в Pull/Merge Request.
type ReviewComment struct {
	// FilePath — путь к файлу, к которому относится комментарий.
	FilePath string `json:"file_path"`
	// Line — номер строки в новой версии файла.
	Line int `json:"line"`
	// Text — текст замечания или рекомендации.
	Text string `json:"text"`
}

// Forge — абстракция над системой хостинга и ревью кода (GitLab, GitHub и т.п.).
// Каждая реализация отвечает за: разбор URL, получение диффа, постинг
// замечаний и одобрение запроса на слияние.
//
// Добавление нового провайдера сводится к реализации этого интерфейса
// и регистрации в фабрике (см. forges.New).
type Forge interface {
	// GetDiff возвращает изменения (diff) запроса на слияние.
	GetDiff() (string, error)

	// PostComment публикует одиночное замечание на конкретной строке.
	// Если привязка к строке невозможна, реализация сама решает,
	// как постить комментарий к файлу.
	PostComment(comment ReviewComment) error

	// Approve одобряет запрос на слияние.
	Approve() error
}

// DetectType определяет тип провайдера по URL.
// Возвращает kind ("gitlab"/"github") или "" если тип не распознан.
func DetectType(prURL string) kind {
	u, err := url.Parse(prURL)
	if err != nil {
		return ""
	}

	host := u.Hostname()

	switch {
	case host == "gitlab.com", host == "gitlab", host == "gitee.com":
		return KindGitLab
	case host == "github.com", host == "github":
		return KindGitHub
	case strings.Contains(host, "gitlab"):
		// Частные/self-hosted GitLab всегда содержат слово "gitlab" в домене,
		// например gitlab.company.com или gitlab.example.org.
		return KindGitLab
	default:
		return ""
	}
}
