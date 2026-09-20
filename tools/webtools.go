package tools

// Инструмент WebSearch — живой поиск в интернете (DuckDuckGo HTML, без
// API-ключа). Модель зовёт его, когда нужна СВЕЖАЯ информация (новости,
// факты, точечная справка, погода — лишь частный пример), которой нет в её
// знаниях или которая могла устареть. Инструмент возвращает топ ссылок
// (заголовок, URL, сниппет), а ответ из них модель извлекает сама.
//
// Degrade: сеть недоступна / поисковая система ответила ошибкой / HTML не
// распознан — инструмент возвращает status degraded с подсказкой ответить из
// знаний модели и честно предупредить пользователя, что данные не живые.
// Это НЕ status error: агентский цикл runner'а не воспринимает degrade как
// провал инструмента, поэтому модель просто получает сигнал «поиск не
// доступен» и действует по промпту.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// WebSearch — имя инструмента в реестре (см. registry.go newTool).
const WebSearch = "WebSearch"

// defaultWebSearchEndpoint — HTML-версия поисковой выдачи DuckDuckGo
// (не требует API-ключа). GET html/?q=<query>.
const defaultWebSearchEndpoint = "https://html.duckduckgo.com/html/"

// webSearchMaxLimit — жёсткий потолок выдачи (лимит отсюда не берётся).
const webSearchMaxLimit = 10

// webSearchUserAgent — «человеко-подобный» User-Agent: DuckDuckGo отвечает
// 403 на дефолтный go-http-client.
const webSearchUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// webSearchResult — один результат поисковой выдачи.
type webSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// webSearchTool — обёртка инструмента WebSearch в реестре.
type webSearchTool struct {
	// client — HTTP-клиент вызовов (nil — http.DefaultClient). Вынесен для
	// hermetic-тестов: подменяется клиентом fake-сервера, сеть не нужна.
	client *http.Client
	// endpoint — адрес страницы поиска (пусто — defaultWebSearchEndpoint).
	// Вынесен для тестов: подменяется адресом fake-сервера.
	endpoint string
}

func (t *webSearchTool) Name() string { return WebSearch }
func (t *webSearchTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name: WebSearch,
		Description: "Используй ЭТОТ инструмент, когда нужна СВЕЖАЯ информация из интернета: новости и текущие события, " +
			"факты, точечная справка, погода, актуальные данные — всё, чего нет в твоих знаниях или что могло устареть. " +
			"Вернёт топ ссылок (заголовок, URL, сниппет) по запросу — ответ из них извлеки сам. Если вернул status degraded " +
			"(сеть/инструмент недоступны) — отвечай из знаний модели и честно предупреди пользователя, что данные не живые.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Поисковый запрос одной строкой (например «погода в Москве сегодня», «последние новости Rust 2026»).",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"description": "Опционально: сколько результатов вернуть (по умолчанию 5, максимум 10).",
				},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
	}
}
func (t *webSearchTool) Execute(args map[string]any) ([]byte, error) { return t.exec(args) }

// WebSearchParams — JSON-параметры инструмента WebSearch.
type WebSearchParams struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

// exec выполняет поиск и возвращает JSON вида
// {"status":"success","query":...,"results":[{title,url,snippet}]}.
// При сетевой ошибке/не-RR-статусе/нераспознанной выдаче — degrade-подсказка
// (status degraded), ошибка Go возвращается только на внутренних сбоях.
func (t *webSearchTool) exec(args map[string]any) ([]byte, error) {
	var p WebSearchParams
	if raw, err := json.Marshal(args); err == nil {
		_ = json.Unmarshal(raw, &p)
	}
	query := strings.TrimSpace(p.Query)
	if query == "" {
		return webSearchJSON(map[string]any{
			"status":  "error",
			"message": "параметр query обязателен и не должен быть пустым",
		}), nil
	}

	s := t.settings()
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.endpoint+"?q="+url.QueryEscape(query), nil)
	if err != nil {
		return webSearchJSON(degradedWebSearch("не удалось сформировать запрос: " + err.Error())), nil
	}
	req.Header.Set("User-Agent", webSearchUserAgent)
	req.Header.Set("Accept-Language", "ru,en;q=0.8")

	resp, err := s.client.Do(req)
	if err != nil {
		// Сетевая ошибка/таймаут — не роняем агентский цикл: degraded даёт
		// модели явный маркер «данные не живые» (см. degradeWebSearch).
		return webSearchJSON(degradedWebSearch("сеть недоступна: " + err.Error())), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return webSearchJSON(degradedWebSearch("поисковая система ответила " + resp.Status)), nil
	}

	results, err := parseDDGResults(resp.Body)
	if err != nil {
		return webSearchJSON(degradedWebSearch("не удалось разобрать ответ поисковой системы: " + err.Error())), nil
	}

	limit := p.MaxResults
	if limit <= 0 {
		limit = s.maxResults
	}
	if limit > webSearchMaxLimit {
		limit = webSearchMaxLimit
	}
	if len(results) > limit {
		results = results[:limit]
	}

	if len(results) == 0 {
		return webSearchJSON(map[string]any{
			"status":  "success",
			"query":   query,
			"results": []webSearchResult{},
			"message": "по запросу ничего не найдено — переформулируй запрос или уточни детали",
		}), nil
	}

	return webSearchJSON(map[string]any{
		"status":  "success",
		"query":   query,
		"results": results,
	}), nil
}

// degradedWebSearch — результат «поиск живьём недоступен»: модель должна
// ответить из знаний и честно предупредить, что данные не живые.
func degradedWebSearch(reason string) map[string]any {
	return map[string]any{
		"status":  "degraded",
		"message": "поиск в интернете недоступен (" + reason + "). Отвечай из знаний модели и честно предупреди пользователя, что данные не живые/могут быть устаревшими.",
	}
}

// webSearchJSON сериализует результат инструмента.
func webSearchJSON(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// webSearchSettings — настройки одного вызова: клиент, endpoint, таймаут и
// лимит выдачи по умолчанию. Пустые значения заменяются значениями по
// умолчанию (клиент — http.DefaultClient, endpoint — DDG).
type webSearchSettings struct {
	client     *http.Client
	endpoint   string
	timeout    time.Duration
	maxResults int
}

func (t *webSearchTool) settings() webSearchSettings {
	s := webSearchSettings{
		client:     t.client,
		endpoint:   t.endpoint,
		timeout:    webSearchTimeout(),
		maxResults: webSearchMaxResults(),
	}
	if s.client == nil {
		s.client = http.DefaultClient
	}
	if s.endpoint == "" {
		s.endpoint = defaultWebSearchEndpoint
	}
	return s
}

// webSearchTimeout — лимит на один запрос поиска (WEB_SEARCH_TIMEOUT), чтобы
// зависшая сеть/поисковая система не вешали шаг агента.
func webSearchTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("WEB_SEARCH_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 15 * time.Second
}

// webSearchMaxResults — лимит выдачи по умолчанию (WEB_SEARCH_MAX_RESULTS),
// по умолчанию 5; потолок — webSearchMaxLimit (из окружения не превышается).
func webSearchMaxResults() int {
	if v := strings.TrimSpace(os.Getenv("WEB_SEARCH_MAX_RESULTS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= webSearchMaxLimit {
			return n
		}
	}
	return 5
}

// parseDDGResults извлекает результаты из HTML-выдачи DuckDuckGo. Разбор по
// токенам (golang.org/x/net/html), без трюков на сырых строках — не зависит
// от мелких особенностей разметки. Структура выдачи: в блоке .result заголовок
// .result__a, ниже — сниппет .result__snippet.
func parseDDGResults(r io.Reader) ([]webSearchResult, error) {
	z := html.NewTokenizer(r)
	var results []webSearchResult
	var cur *webSearchResult
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			if z.Err() == io.EOF {
				if cur != nil && cur.Title != "" {
					results = append(results, *cur)
				}
				return results, nil
			}
			return nil, z.Err()
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			if string(name) != "a" {
				continue
			}
			var href, cls string
			for hasAttr {
				key, val, more := z.TagAttr()
				switch string(key) {
				case "href":
					href = string(val)
				case "class":
					cls = string(val)
				}
				hasAttr = more
			}
			switch {
			case strings.Contains(cls, "result__a"):
				// Новый результат: закрываем предыдущий и открываем текущий.
				if cur != nil && cur.Title != "" {
					results = append(results, *cur)
				}
				cur = &webSearchResult{
					Title: normalizeWebSearchText(readAnchorText(z)),
					URL:   decodeDDGHref(href),
				}
			case strings.Contains(cls, "result__snippet") && cur != nil:
				if cur.Snippet == "" {
					cur.Snippet = normalizeWebSearchText(readAnchorText(z))
				}
			}
		}
	}
}

// readAnchorText вычитывает текст внутри <a>…</a>: токены текста до закрытия
// тега. html-сущности декодирует токенайзер.
func readAnchorText(z *html.Tokenizer) string {
	var b strings.Builder
	for {
		tt := z.Next()
		switch tt {
		case html.TextToken:
			b.Write(z.Text())
		case html.EndTagToken:
			name, _ := z.TagName()
			if string(name) == "a" {
				return b.String()
			}
		case html.ErrorToken:
			return b.String()
		}
	}
}

// normalizeWebSearchText снимает HTML-сущности и схлопывает пробелы/переносы.
func normalizeWebSearchText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

// decodeDDGHref разворачивает редирект DuckDuckGo
// (//duckduckgo.com/l/?uddg=<url>&rut=…) в реальный адрес; обычные ссылки, в
// том числе относительные "//…", поправляются до https.
func decodeDDGHref(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	if u, err := url.Parse(href); err == nil {
		if d := u.Query().Get("uddg"); d != "" {
			return d
		}
	}
	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}
	return href
}
