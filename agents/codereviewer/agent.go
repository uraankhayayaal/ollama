package codereviewer

import (
	"ai/agents"
	"ai/forges"
	"ai/langdetect"
	"ai/logging"
	"ai/tools"
	"fmt"
	"os"
	"strings"

	"github.com/ollama/ollama/api"
)

// reviewToolNames — инструменты, которые ревьюер выбирает из общего реестра
// tools. Реализации (ReviewMr/ApproveMr/NextChunk) живут в пакете tools и
// работают с общим *tools.ReviewSession (состояние цикла ревью).
var reviewToolNames = []string{"ReviewMr", "ApproveMr", "NextChunk"}

type Codereviewer struct {
	// *tools.ReviewSession — разделяемое состояние цикла ревью (diff, чанки,
	// счётчики, forge). Поля и методы промотируются: cr.Forge, cr.Diff,
	// cr.ChunkIdx и т.п. доступны напрямую.
	*tools.ReviewSession
	IsUseMemory bool
	cfg         Config
	// focus — цель ревью из аргумента CLI (например "безопасность").
	focus string
	// NoChunk отключает разбиение диффа на части: ревьювер получает весь
	// изменённый код одним сообщением. Используется для провайдеров с одним
	// раундом (например, trim), где цикл NextChunk недоступен.
	NoChunk bool
	// Tools — выбранные ревьюером инструменты из общего реестра
	// (единый источник для GetTools/GetToolsForOllama и диспетчеризации вызовов).
	Tools *tools.Set
}

// newCodereviewer собирает агента вокруг общего сеанса ревью: выбирает из
// реестра инструменты ревью и связывает их с сеансом, чтобы состояние
// (diff, чанки, счётчики) жило между вызовами на протяжении цикла.
func newCodereviewer(ses *tools.ReviewSession, cfg Config, focus string) *Codereviewer {
	return &Codereviewer{
		ReviewSession: ses,
		IsUseMemory:   true,
		cfg:           cfg,
		focus:         focus,
		Tools:         tools.Select(reviewToolNames, tools.Deps{Session: ses}),
	}
}

// NewCodereviewer создаёт агента код-ревью. Ссылка может указывать на
// любой поддерживаемый хостинг (GitLab/GitHub) — тип определяется по URL,
// а токен берётся из переменной окружения <HOST>_TOKEN (см. forges.New).
//
// args[0] — URL MR/PR. args[1] — необязательная цель ревью (например
// "безопасность", "производительность"), подставляется в промпт.
func NewCodereviewer(args []string) *Codereviewer {
	if len(args) < 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "Ошибка: укажите ссылку на MR, например: go run . review <URL>")
		os.Exit(1)
	}
	prURL := args[0]

	focus := ""
	if len(args) >= 2 && args[1] != "" {
		focus = args[1]
	}

	token := pickToken(prURL)
	forge, err := forges.New(prURL, token)
	if err != nil {
		logging.Fatalf("Ошибка создания провайдера ревью: %v", err)
	}

	cfg := LoadConfig()
	ses := &tools.ReviewSession{
		Forge:           forge,
		MaxComments:     cfg.MaxComments,
		BlockOnCritical: cfg.BlockOnCritical,
		Focus:           focus,
	}
	return newCodereviewer(ses, cfg, focus)
}

// NewCodereviewerWithForge создаёт агента код-ревью с уже готовой
// реализацией forges.Forge (например, для local self-review). Позволяет
// переиспользовать логику ревью без обращения к реальному хостингу.
func NewCodereviewerWithForge(forge forges.Forge, focus string) *Codereviewer {
	cfg := LoadConfig()
	ses := &tools.ReviewSession{
		Forge:           forge,
		MaxComments:     cfg.MaxComments,
		BlockOnCritical: cfg.BlockOnCritical,
		Focus:           focus,
	}
	return newCodereviewer(ses, cfg, focus)
}

// pickToken выбирает токен доступа в зависимости от хостинга:
// GitLab → GITLAB_TOKEN, GitHub → GITHUB_TOKEN.
func pickToken(prURL string) string {
	switch forges.DetectType(prURL) {
	case forges.KindGitHub:
		return os.Getenv("GITHUB_TOKEN")
	case forges.KindGitLab:
		return os.Getenv("GITLAB_TOKEN")
	default:
		return ""
	}
}

func (cr *Codereviewer) GetUserMessages() []agents.Message {
	diff, err := cr.Forge.GetDiff()
	if err != nil {
		// Не убиваем весь процесс при сбое получения диффа: сообщаем
		// ошибку моделью через user-сообщение, чтобы агент завершился,
		// не пытаясь постить замечания к несуществующему коду.
		return []agents.Message{
			{
				Type: agents.MessageTypeHuman,
				Message: fmt.Sprintf("Не удалось получить изменения кода: %v. "+
					"Заверши ревью без вызова инструментов и объясни причину.", err),
			},
		}
	}

	// Если включено — отсекаем сгенерированные и бинарные файлы, чтобы
	// не тратить контекст модели и не комментировать артефакты.
	if cr.cfg.SkipGenerated {
		diff = tools.FilterGeneratedDiff(diff)
	}

	// Полный дифф кэшируем в сеансе для детекции языка и валидации
	// комментариев.
	cr.Diff = diff

	// Пустой (или полностью отфильтрованный) дифф — завершаем без ревью.
	if strings.TrimSpace(diff) == "" {
		return []agents.Message{
			{
				Type: agents.MessageTypeHuman,
				Message: "Изменений для ревью нет (дифф пуст, либо содержит только " +
					"сгенерированные/бинарные файлы). Заверши ревью без вызова инструментов.",
			},
		}
	}

	// Разбиваем большой дифф на части, чтобы не переполнять контекст:
	// ревью идёт по чанкам, между которыми модель вызывает NextChunk.
	// Для провайдеров с одним раундом (NoChunk) дифф передаётся целиком.
	maxChars := cr.cfg.ChunkSize
	if cr.NoChunk {
		maxChars = 0
	}
	cr.SetDiff(diff, maxChars)

	msg := "Изменения кода"
	if len(cr.Chunks) > 1 {
		msg += " (часть " + tools.ChunkLabel(cr.ChunkIdx, len(cr.Chunks)) + ")"
	}
	msg += ": " + cr.Chunks[0] + ". Просмотри эту часть и вызови ReviewMr для каждого замечания " +
		"(file_path, line — точный номер из диффа, text)"
	if len(cr.Chunks) > 1 {
		msg += ", затем NextChunk, чтобы получить следующую часть"
	}
	msg += ". НЕ пиши ревью текстом — только вызовы инструментов."

	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: msg,
		},
	}
}

// diffFromMessages извлекает текст диффа из переданных user-сообщений.
// Дифф приходит от раннера в GetSystemMessages вместе с сообщениями.
func diffFromMessages(messages []agents.Message) string {
	for _, m := range messages {
		if m.Type == agents.MessageTypeHuman {
			return m.Message
		}
	}
	return ""
}

// GetTools возвращает определения выбранных инструментов (единый JSON Schema
// формат для OpenAI/Yandex). Источник схем — реестр tools.
func (cr Codereviewer) GetTools() []tools.ToolDefinition {
	return cr.Tools.Definitions()
}

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama (без ручного дублирования схем).
func (cr Codereviewer) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(cr.GetTools())
}

func (cr *Codereviewer) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return cr.Tools.Execute(functionName, functionArgs)
}

// Обёртки инструментов ревью. Тело живёт в tools.ReviewSession; наблюдаемые
// снаружи сигнатуры сохранены (используются тестами и main.go) и делегируют
// в реестр.

func (cr *Codereviewer) ReviewMr(args map[string]any) []byte {
	out, _ := cr.Tools.Execute("ReviewMr", args)
	return out
}

func (cr *Codereviewer) ApproveMr(args map[string]any) []byte {
	out, _ := cr.Tools.Execute("ApproveMr", args)
	return out
}

func (cr *Codereviewer) NextChunk() []byte {
	out, _ := cr.Tools.Execute("NextChunk", map[string]any{})
	return out
}

func (cr *Codereviewer) PublishParsedReview(content string) int {
	return cr.ReviewSession.PublishParsedReview(content)
}

func (cr *Codereviewer) PostSummaryToPR() error {
	return cr.ReviewSession.PostSummaryToPR()
}

func (cr *Codereviewer) GetSystemMessages(text []agents.Message) []agents.Message {
	if cr.IsUseMemory {
		// Язык определяем по полному диффу (он кэшируется при разбиении
		// диффа на чанки); если его нет — по тексту user-сообщений.
		diffForLang := cr.Diff
		if diffForLang == "" {
			diffForLang = diffFromMessages(text)
		}

		// Определяем язык по диффу.
		language := langdetect.Detect(diffForLang)

		// Для PHP дополнительно указываем фреймворк (Laravel), сохраняя
		// все прежние правила проекта (PSR-12, SOLID, lighthouse, DTO и пр.).
		framework := ""
		if language == langdetect.PHP {
			framework = "Laravel"
		}

		prompt := langdetect.ReviewPrompt(language, framework)

		// 5. Цель ревью из аргумента CLI — смещает фокус модели.
		if cr.focus != "" {
			prompt += fmt.Sprintf(
				"\n\t\tОсобый фокус ревью: %s. Удели этому аспекту приоритетное внимание.",
				cr.focus)
		}

		// Разъясняем семантику критичности для блокировки апрува (Feature 2).
		prompt += "\n\t\tЗамечание считается 'критично:', если оно нарушает работу приложения или безопасность. " +
			"Если есть хотя бы одно критическое замечание — НЕ вызывай ApproveMr."

		// Схождение ревью: ревьювер работает только в своей области видимости
		// и не пытается править код или подсказывать правку в других файлах.
		// Если он уже опубликовал критические замечания, а ApproveMr запрещён
		// — ревью считается завершённым: завершай цикл текстом-сводкой, а не
		// повторяй те же инструменты (это зацикливает агента до лимита).
		prompt += "\n\t\tРевьювер не правит код и не заглядывает за пределы своей области видимости. " +
			"Если критические замечания уже опубликованы (ApproveMr запрещён) — ревью завершено: " +
			"не вызывай ApproveMr и NextChunk повторно, а заверши текстовой сводкой. " +
			"Не пытайся «исправить» код — это задача другого агента."

		// Протокол ревью по частям (Feature): когда дифф передан чанками,
		// модель должна обрабатывать по одному чанку за раз и двигаться
		// через NextChunk, а не ApproveMr до просмотра всех частей.
		if len(cr.Chunks) > 1 {
			prompt += "\n\t\tДифф передан по частям. Обрабатывай ровно одну часть за раз: " +
				"сначала ReviewMr по текущей части, затем NextChunk для следующей. " +
				"ApproveMr вызывай только после получения сообщения о просмотре всех частей."
		}

		// Жёсткое требование: результат только через инструменты. Модели
		// (особенно YandexGPT) склонны писать ревью текстом вместо вызова
		// инструментов — тогда замечания не попадают в MR.
		prompt += "\n\t\tЗапрещено писать ревью текстом в ответе. " +
			"Каждое замечание обязательно публикуется вызовом ReviewMr. " +
			"После просмотра всех частей обязательно вызови ApproveMr (или верни отказ, если есть критичные). " +
			"Если не вызвал ни одного инструмента до записи текстового ответа — ревью считается невыполненным."

		return []agents.Message{
			{
				Type:    agents.MessageTypeSystem,
				Message: prompt,
			},
		}
	}

	return []agents.Message{}
}
