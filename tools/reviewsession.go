package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"ai/forges"
)

// ReviewSession — разделяемое состояние цикла код-ревью, которое живёт между
// вызовами инструментов (ReviewMr/ApproveMr/NextChunk) в течение всего цикла
// агента. Агент создаёт его один раз, а инструменты читают/мутируют поля.
type ReviewSession struct {
	// Forge — система хостинга, куда публикуются замечания и апрув.
	Forge forges.Forge
	// MaxComments ограничивает число публикуемых замечаний за одно ревью.
	MaxComments int
	// BlockOnCritical запрещает апрув при наличии критичных замечаний.
	BlockOnCritical bool
	// Focus — цель ревью из аргумента CLI (например "безопасность").
	Focus string

	// Diff — актуальный дифф, кэшируется при разбиении на чанки и
	// используется в ReviewMr для отсечения галлюцинирующих комментариев.
	Diff string
	// Chunks — дифф, разбитый на части для ревью по частям.
	Chunks []string
	// ChunkIdx — индекс текущего обрабатываемого чанка.
	ChunkIdx int

	// PostErrors накапливает ошибки публикации комментариев в ReviewMr.
	PostErrors []string
	// CriticalFound — были ли опубликованы критичные замечания ("критично:").
	CriticalFound bool
	// CommentCount — сколько замечаний опубликовано за это ревью.
	CommentCount int
	// RejectedCount — сколько комментариев отсечено проверкой по diff.
	RejectedCount int

	// summaryPosted — публиковался ли уже итоговый отчёт в тред.
	summaryPosted bool
	// dedupSeen — уже опубликованные локации/сигнатуры замечаний.
	dedupSeen map[string]bool
}

// SetDiff кэширует дифф и разбивает его на чанки для ревью по частям.
// maxChars <= 0 отключает разбиение (дифф передаётся целиком).
// Используется агентом в GetUserMessages перед запуском цикла ревью.
func (s *ReviewSession) SetDiff(diff string, maxChars int) {
	s.Diff = diff
	s.Chunks = SplitDiffChunks(diff, maxChars)
	s.ChunkIdx = 0
}

// IsCritical определяет, помечено ли замечание как критичное.
// Критичность задаётся префиксом "критично:" в тексте комментария.
func IsCritical(text string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "критично:")
}

// ReviewMr публикует замечания, отсекая галлюцинации (по diff) и дубликаты.
func (s *ReviewSession) ReviewMr(args map[string]any) ([]byte, error) {
	var comments []forges.ReviewComment

	// comments приходит от разных провайдеров: иногда как JSON-строка
	// (Yandex), иногда уже как разобранный слайс. Парсим оба варианта.
	comments = parseComments(args["comments"])

	// Отсекаем галлюцинирующие замечания — к файлам/строкам, которых нет
	// в актуальном диффе. Такие комментарии GitHub/GitLab всё равно не
	// примут (422), а модель может насочинять несуществующие строки.
	// Отклонённые не блокируют апрув, но сообщаются модели в ответе.
	var rejected []string
	if s.Diff != "" {
		comments, rejected = FilterCommentsByDiff(s.Diff, comments)
		s.RejectedCount += len(rejected)
	}

	// Фильтруем подозрительные комментарии с русским "это хорошо, но..." паттерном
	comments = filterSuspiciousComments(comments)

	// Группируем и удаляем дубликаты по тексту замечания и файлам
	if len(comments) > 0 {
		comments = groupSimilarComments(comments)
	}

	// Дедупек однотипных замечаний: одно и то же замечание, повторённое
	// в разных местах или раундах, публикуется только один раз. Карта
	// сохраняется на сессии и накапливается между раундами.
	if len(comments) > 0 {
		if s.dedupSeen == nil {
			s.dedupSeen = map[string]bool{}
		}
		comments = DedupComments(comments, s.dedupSeen)
	}

	// Лимит замечаний за одно ревью: оставляем только первые MaxComments.
	if s.MaxComments > 0 && len(comments) > s.MaxComments {
		comments = comments[:s.MaxComments]
	}

	// Публикуем каждое замечание. При сбое фиксируем ошибку на сессии,
	// чтобы ApproveMr в последующем раунде отказался одобрять изменения.
	result := []map[string]string{}
	for _, comment := range comments {
		// Отмечаем критичные замечания для блокировки апрува.
		if IsCritical(comment.Text) {
			s.CriticalFound = true
		}

		err := s.Forge.PostComment(comment)
		if err != nil {
			s.PostErrors = append(s.PostErrors, err.Error())
			result = append(result, map[string]string{"status": "error", "message": err.Error()})
			continue
		}
		s.CommentCount++
		result = append(result, map[string]string{"status": "ok"})
	}

	// Передаём модели сведения об отсечённых галлюцинациях, чтобы она
	// не повторяла их в следующих раундах.
	if len(rejected) > 0 {
		result = append(result, map[string]string{
			"status":  "warning",
			"message": fmt.Sprintf("отсечено галлюцинирующих замечаний: %d", len(rejected)),
		})
	}

	// Кодируем результат обратно в JSON для модели (Tool message).
	resultJSON, _ := json.Marshal(result)
	return resultJSON, nil
}

// ApproveMr одобряет изменения, если нет критичных замечаний и ошибок
// публикации.
func (s *ReviewSession) ApproveMr(args map[string]any) ([]byte, error) {
	result := map[string]string{}

	// Не одобряем, если есть критические замечания (блокирующие).
	if s.BlockOnCritical && s.CriticalFound {
		result = map[string]string{
			"status":  "error",
			"message": "нельзя одобрить: есть критические замечания",
		}
		resultJSON, _ := json.Marshal(result)
		return resultJSON, nil
	}

	// Не одобряем, если какое-то замечание не удалось опубликовать.
	if len(s.PostErrors) > 0 {
		result = map[string]string{
			"status":  "error",
			"message": fmt.Sprintf("нельзя одобрить: %d замечание(й) не были обработаны", len(s.PostErrors)),
		}
		resultJSON, _ := json.Marshal(result)
		return resultJSON, nil
	}

	// Собираем легенду апрува (что проверено) и прикладываем к одобрению.
	legend := fmt.Sprintf("Ревью пройдено. Опубликовано замечаний: %d. Изменений принимаю.",
		s.CommentCount)

	err := s.Forge.Approve(legend)
	if err != nil {
		result = map[string]string{"status": "error", "message": err.Error()}
	}

	resultJSON, _ := json.Marshal(result)
	return resultJSON, nil
}

// NextChunk возвращает следующий чанк диффа для ревью, если такой есть.
// Вызывается моделью после обработки текущего чанка (ревью по частям
// большого диффа). Если это был последний чанк — сообщает модели, что пора
// принять решение через ApproveMr.
func (s *ReviewSession) NextChunk() ([]byte, error) {
	if s.ChunkIdx+1 >= len(s.Chunks) {
		out, _ := json.Marshal(map[string]string{
			"status":  "done",
			"message": "все части диффа просмотрены. Прими окончательное решение: вызови ApproveMr, если критичных замечаний нет.",
		})
		return out, nil
	}

	s.ChunkIdx++
	out, _ := json.Marshal(map[string]string{
		"status": "ok",
		"diff": "Изменения кода (часть " + ChunkLabel(s.ChunkIdx, len(s.Chunks)) + "): " + s.Chunks[s.ChunkIdx] +
			". Просмотри эту часть, вызови ReviewMr при необходимости, затем NextChunk для следующей.",
	})
	return out, nil
}

// PublishParsedReview публикует текстовое ревью, преобразуя его в комментарии
// через стандартный ReviewMr-путь (включая фильтр по diff и дедупек).
// Модели, склонные писать текст вместо вызова инструментов, теряют замечания:
// этот метод спасает результат. Возвращает число опубликованных замечаний.
func (s *ReviewSession) PublishParsedReview(content string) int {
	if strings.TrimSpace(content) == "" {
		return 0
	}
	comments := ParseTextReview(content)
	if len(comments) == 0 {
		return 0
	}
	// Кодируем в JSON-строку, т.к. parseComments принимает этот формат.
	b, err := json.Marshal(comments)
	if err != nil {
		return 0
	}
	// Используем ReviewMr, чтобы применить фильтр по diff и дедупликацию.
	s.ReviewMr(map[string]any{"comments": string(b)})
	return s.CommentCount
}

// groupSimilarComments объединяет похожие замечания по тексту, оставляя только одно
// замечание для одинаковых проблем в разных файлах
func groupSimilarComments(comments []forges.ReviewComment) []forges.ReviewComment {
	if len(comments) <= 1 {
		return comments
	}

	// Группируем по тексту замечания
	type commentGroup struct {
		text    string
		files   []string
		lines   []int
		comment forges.ReviewComment
	}

	groups := make(map[string]*commentGroup)
	for _, comment := range comments {
		// Используем текст замечания как ключ для группировки
		text := strings.TrimSpace(comment.Text)
		
		if group, exists := groups[text]; exists {
			// Добавляем файл к существующей группе
			group.files = append(group.files, comment.FilePath)
			group.lines = append(group.lines, comment.Line)
		} else {
			// Создаём новую группу
			groups[text] = &commentGroup{
				text:    text,
				files:   []string{comment.FilePath},
				lines:   []int{comment.Line},
				comment: comment,
			}
		}
	}

	// Если в группе более одного файла, объединяем в одно замечание
	var result []forges.ReviewComment
	for _, group := range groups {
		if len(group.files) > 1 {
			// Создаем объединённое замечание с упоминанием всех файлов
			group.comment.FilePath = ""
			group.comment.Text = fmt.Sprintf("%s (в %d файлах: %s)", 
				group.text, 
				len(group.files), 
				strings.Join(group.files, ", "))
		}
		result = append(result, group.comment)
	}

	return result
}

// filterSuspiciousComments удаляет комментарии, которые соответствуют русскому
// паттерну "это хорошо, но стоит убедиться, что ..." или аналогичному, чтобы
// избежать ненужных/непродуктивных замечаний.
func filterSuspiciousComments(comments []forges.ReviewComment) []forges.ReviewComment {
	var filtered []forges.ReviewComment
	
	for _, comment := range comments {
		text := strings.TrimSpace(comment.Text)
		
		// Проверяем, не соответствует ли текст русскому паттерну
		// "это хорошо, но стоит убедиться, что ..."
		if strings.Contains(strings.ToLower(text), "это хорошо") && 
		   strings.Contains(strings.ToLower(text), "стоит убедиться, что") {
			// Пропускаем такие комментарии
			continue
		}
		
		// Также проверяем другие подобные конструкции
		if strings.Contains(strings.ToLower(text), "это хорошо, но") &&
		   strings.Contains(strings.ToLower(text), "стоит убедиться") {
			// Пропускаем такие комментарии
			continue
		}
		
		// Проверяем на признаки предложений о проверке ("убедиться", "увериться")
		if strings.Contains(strings.ToLower(text), "убедиться") ||
		   strings.Contains(strings.ToLower(text), "увериться") {
			// Пропускаем такие комментарии - они не указывают на ошибки
			continue
		}
		
		filtered = append(filtered, comment)
	}
	
	return filtered
}

// PostSummaryToPR публикует итоговый отчёт-сводку в тред MR/PR.
// Вызывается раннером после завершения цикла агента, чтобы команда видела
// сводку ревью даже при частичном результате.
func (s *ReviewSession) PostSummaryToPR() error {
	if s.summaryPosted {
		return nil
	}

	var b strings.Builder
	b.WriteString("## Итоги код-ревью\n\n")
	fmt.Fprintf(&b, "- Замечаний опубликовано: **%d**\n", s.CommentCount)
	fmt.Fprintf(&b, "- Критических замечаний: **%s**\n", yesNo(s.CriticalFound))
	fmt.Fprintf(&b, "- Ошибок публикации: **%d**\n", len(s.PostErrors))
	fmt.Fprintf(&b, "- Отсечено галлюцинирующих замечаний: **%d**\n", s.RejectedCount)
	if s.Focus != "" {
		fmt.Fprintf(&b, "- Фокус ревью: **%s**\n", s.Focus)
	}
	if len(s.PostErrors) > 0 {
		b.WriteString("\n> Часть замечаний не была опубликована. Проверьте лог агента.\n")
	}

	if err := s.Forge.PostSummary(b.String()); err != nil {
		return err
	}
	s.summaryPosted = true
	return nil
}

func yesNo(v bool) string {
	if v {
		return "да"
	}
	return "нет"
}
