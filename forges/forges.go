package forges

import (
	"errors"
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

	// CreateMergeRequest создаёт запрос на слияние по параметрам и
	// возвращает ссылку на созданный MR/PR. Применяется для HITL-затвора
	// «Принять → MR» (Ф-2-3): агент подготовил фича-ветку и отправляет её
	// человеку на одобрение слияния.
	//
	// Передаваемый при конструировании URL не обязан быть «живым» запросом
	// на слияние — конструктор в этом случае должен позволить создание MR
	// по параметрам (переопределяется реализациями).
	CreateMergeRequest(opts MergeRequestOptions) (string, error)
}

// MergeRequestOptions — параметры создания Merge/Pull Request.
type MergeRequestOptions struct {
	// SourceBranch — имя исходной (фича) ветки.
	SourceBranch string `json:"source_branch"`
	// TargetBranch — целевая ветка, в которую вливается фича.
	TargetBranch string `json:"target_branch"`
	// Title — заголовок запроса.
	Title string `json:"title"`
	// Description — описание (легенда правки, итог кратко).
	Description string `json:"description"`
}

// MergeRequestInfo — описание существующего Merge/Pull Request (Ф-5):
// ссылка и текущее состояние. Используется сверкой «есть ли MR по ветке»
// при открытии дашборда, когда сам MR мог быть создан и вне нашего UI.
type MergeRequestInfo struct {
	// URL — ссылка на MR/PR в web-интерфейсе хостинга.
	URL string
	// State — состояние: open|merged|closed.
	State string
}

// ErrNoMergeRequest — у ветки с заданными source/target нет открытого MR.
var ErrNoMergeRequest = errors.New("merge request по ветке не найден")

// MRStatusProvider — опциональный интерфейс форджа: поиск MR по паре
// веток source/target. Не входит в базовый Forge, чтобы не ломать моки и
// локальные реализации (LocalForge MR не знает): сервер делает type-assert.
type MRStatusProvider interface {
	// FindMergeRequest ищет MR с веткой-источником source / целью target
	// (target может быть пустым для части провайдеров) и возвращает ссылку
	// и состояние. ErrNoMergeRequest, если такого MR нет.
	FindMergeRequest(source, target string) (*MergeRequestInfo, error)
}

// DetectType определяет тип провайдера по URL.
// Возвращает kind ("gitlab"/"github"/"local") или "" если тип не распознан.
// Входит в работу SSH-remote в SCP-виде (git@host:owner/repo.git) и
// обычные URL (https://…, ssh://git@host/…).
func DetectType(prURL string) kind {
	// Локальная директория адресуется схемой local://<path>.
	if strings.HasPrefix(prURL, localSchemeLegacy) {
		return KindLocal
	}

	// SSH-SCP-вид git@host:owner/repo.git схемы не имеет: урл.парс его
	// возьмёт как относительный path, и хост получится пустым.
	if strings.HasPrefix(prURL, "git@") {
		scp := strings.TrimPrefix(prURL, "git@")
		if i := strings.IndexByte(scp, ':'); i > 0 {
			return kindForHost(scp[:i])
		}
		return ""
	}

	u, err := url.Parse(prURL)
	if err != nil {
		return ""
	}

	return kindForHost(u.Hostname())
}

// kindForHost маппит хост на тип провайдера.
func kindForHost(host string) kind {
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
