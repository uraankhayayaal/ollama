package forges

import (
	"net/url"
	"sort"
	"strconv"
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

	// PostSummary публикует итоговый отчёт-сводку в общий тред запроса
	// слияния (например, сводку ревью: число критичных/оставшихся замечаний).
	PostSummary(summary string) error

	// Approve одобряет запрос на слияние. summary — текст комментария,
	// прикладываемый к одобрению (легенда ревью, может быть пустым).
	Approve(summary string) error
}

// DetectType определяет тип провайдера по URL.
// Возвращает kind ("gitlab"/"github"/"local") или "" если тип не распознан.
func DetectType(prURL string) kind {
	// Локальная директория адресуется схемой local://<path>.
	if strings.HasPrefix(prURL, localSchemeLegacy) {
		return KindLocal
	}

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

// CommentSignature возвращает детерминированную сигнатуру набора замечаний
// по их расположению (файл:строка). Используется циклами self-repair, чтобы
// определить, продвинулось ли исправление: если замечания между раундами
// приходятся на те же места, фикс не дал результата и цикл пора прерывать.
func CommentSignature(comments []ReviewComment) string {
	keys := make([]string, 0, len(comments))
	for _, c := range comments {
		keys = append(keys, commentKey(c))
	}
	sort.Strings(keys)
	return strings.Join(keys, ";")
}

// commentKey строит стабильный ключ замечания "путь:строка".
func commentKey(c ReviewComment) string {
	return c.FilePath + ":" + strconv.Itoa(c.Line)
}
