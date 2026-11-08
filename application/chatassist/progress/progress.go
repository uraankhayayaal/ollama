// Package progress — отслеживание прогресса по подзадачам в рамках сессии чата.
//
// Цель: при сжатии истории диалога (убирании середины) не терять информацию о
// том, что именно было сделано. ProgressTracker парсит сообщения ассистента и
// результаты инструментов, извлекает подзадачи и их статус, и генерирует
// компактное резюме прогресса, которое вставляется в системный промпт при
// сжатии — модель знает, что уже выполнено, а что осталось.

package progress

import (
	"regexp"
	"strings"
)

// Status — статус подзадачи.
type Status int

const (
	StatusPending Status = iota // ожидает
	StatusWorking               // в работе
	StatusDone                  // выполнено
	StatusFailed                // не выполнено / ошибка
	StatusSkipped               // пропущено / неактуально
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "ожидает"
	case StatusWorking:
		return "в работе"
	case StatusDone:
		return "✓ выполнено"
	case StatusFailed:
		return "✗ ошибка"
	case StatusSkipped:
		return "— пропущено"
	default:
		return "?"
	}
}

// Item — одна отслеживаемая подзадача.
type Item struct {
	Name   string // краткое описание подзадачи
	Status Status
	Note   string // деталь (файл, причина ошибки); пусто — без деталей
}

// Summary — сводка прогресса сессии.
type Summary struct {
	Items []Item
}

// Text формирует компактное текстовое представление резюме.
func (s *Summary) Text() string {
	if len(s.Items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Прогресс по подзадачам (с жатием истории):\n")
	for _, it := range s.Items {
		b.WriteString("  ")
		b.WriteString(it.Status.String())
		b.WriteString(" — ")
		b.WriteString(it.Name)
		if it.Note != "" {
			b.WriteString(": ")
			b.WriteString(it.Note)
		}
		b.WriteByte('\n')
	}
	out := b.String()
	if len(out) > MaxSummaryChars {
		out = out[:MaxSummaryChars] + "\n[...резюме обрезано]"
	}
	return out
}

// MaxSummaryChars — максимум символов в резюме прогресса.
const MaxSummaryChars = 2 * 1024

// ProgressTracker — отслеживает подзадачи в ходе диалога.
// Используется при сжатии истории: парсит убранную середину на предмет
// завершённых/незавершённых шагов и выдаёт резюме.
type ProgressTracker struct {
	items []Item
}

// New создаёт трекер с пустым списком подзадач.
func New() *ProgressTracker {
	return &ProgressTracker{}
}

// AddItem добавляет подзадачу (если ещё нет такой).
func (t *ProgressTracker) AddItem(name string, status Status, note string) {
	canon := canonicalize(name)
	for i := range t.items {
		if canonicalize(t.items[i].Name) == canon {
			if status == StatusDone {
				t.items[i].Status = StatusDone
			}
			if t.items[i].Note == "" {
				t.items[i].Note = note
			}
			return
		}
	}
	t.items = append(t.items, Item{Name: name, Status: status, Note: note})
}

// UpdateStatus обновляет статус существующей подзадачи.
func (t *ProgressTracker) UpdateStatus(name string, status Status, note string) {
	canon := canonicalize(name)
	for i := range t.items {
		if canonicalize(t.items[i].Name) == canon {
			t.items[i].Status = status
			if t.items[i].Note == "" {
				t.items[i].Note = note
			}
			return
		}
	}
}

// Summary возвращает текущую сводку.
func (t *ProgressTracker) Summary() *Summary {
	return &Summary{Items: t.items}
}

// ExtractFromMessages — основной пайплайн: парсит сообщения диалога (ассистент
// + результаты инструментов) и извлекает подзадачи со статусами.
// Работает на убранной «середине» при сжатии.
func ExtractFromMessages(msgs []MessageData) *Summary {
	t := New()
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			parseAssistantMsg(t, m.Content)
		case "tool":
			parseToolResult(t, m.ToolName, m.Content)
		}
	}
	return t.Summary()
}

// MessageData — минимальный набор полей сообщения для парсинга.
type MessageData struct {
	Role     string
	Content  string
	ToolName string // только для role=tool
}

// numberedRe — номер + текст: "1. проверить сборку", "3) правка хендлеров".
var numberedRe = regexp.MustCompile(`(?m)^[\s]*\d+[.)\]]\s+(.+)`)

// bulletRe — пули: "- проверить", "* исправить", "• запустить тесты".
var bulletRe = regexp.MustCompile(`(?m)^[\s]*[-•*]\s+(.+)`)

// donePatterns — маркеры завершения в тексте ассистента.
var donePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(исправлен(о|а|ы)?|выполнен(о|а|ы)?|сделан(о|а|ы)?|завершен(о|а|ы)?)`),
	regexp.MustCompile(`(?i)(PASS|ok |✓|✔|✅|\bfix(es|ed)|\bsuccess|completed?\b)`),
}

// failPatterns — маркеры неудачи.
var failPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(ошибк(а|и|у)|error|FAIL|panic|не\s*(удался?|работает?|нашёл?)|cannot|\bfails?\b)`),
	regexp.MustCompile(`(?i)(✗|❌|skipped|не\s*собрался|не\s*прошёл)`),
}

// actionVerbs — начальные глаголы для нормализации подзадач.
var actionVerbs = map[string]bool{
	"провер": true, "правка": true, "исправ": true, "созда": true,
	"убрать": true, "запусти": true, "добав": true,
	"удал": true, "поиск": true, "читать": true, "чтение": true,
	"анализ": true, "анализируй": true, "найти": true, "найди": true,
	"разобрать": true, "разберись": true, "собрать": true, "закоммит": true,
	"переимен": true, "перемести": true, "обнови": true,
}

func parseAssistantMsg(t *ProgressTracker, content string) {
	// Извлекаем нумерованные списки.
	for _, m := range numberedRe.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			task := strings.TrimSpace(m[1])
			if len(task) > 3 && len(task) < 200 {
				t.AddItem(task, StatusWorking, "")
			}
		}
	}

	// Пулевые списки.
	for _, m := range bulletRe.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			task := strings.TrimSpace(m[1])
			if len(task) > 3 && len(task) < 200 {
				t.AddItem(task, StatusWorking, "")
			}
		}
	}

	// Маркеры завершения в тексте.
	for _, re := range donePatterns {
		if re.MatchString(content) {
			// Помечаем все «в работе» как выполненные, если в тексте
			// явно говорится об успехе — но только если есть конкретика.
			for i := range t.items {
				if t.items[i].Status == StatusWorking {
					// Консервативно: помечаем как done только если
					// имя подзадачи встречается в этом сообщении.
					if strings.Contains(content, t.items[i].Name) ||
						containsVerb(content, t.items[i].Name) {
						t.items[i].Status = StatusDone
					}
				}
			}
		}
	}
}

// containsVerb проверяет, содержит ли текст глагол, относящийся к подзадаче.
func containsVerb(text, taskName string) bool {
	words := strings.Fields(taskName)
	for _, w := range words {
		w = strings.ToLower(w)
		for verb := range actionVerbs {
			if strings.HasPrefix(w, verb) {
				if strings.Contains(strings.ToLower(text), w) {
					return true
				}
			}
		}
	}
	return false
}

func parseToolResult(t *ProgressTracker, toolName, content string) {
	hasFail := strings.Contains(content, `"status":"error"`) ||
		strings.Contains(content, `"status": "error"`) ||
		strings.Contains(content, `"status":"fail"`) ||
		strings.Contains(content, `"status":"failed"`)

	status := StatusDone
	if hasFail {
		status = StatusFailed
	}

	for i := range t.items {
		if t.items[i].Status == StatusWorking {
			toolLower := strings.ToLower(toolName)
			taskLower := strings.ToLower(t.items[i].Name)

			// Сопоставление по инструменту: если задача про сборку и
			// инструмент Run вернул ок → вероятно выполнено.
			related := false
			switch {
			case strings.Contains(taskLower, "сборк") && toolLower == "run":
				related = true
			case strings.Contains(taskLower, "тест") && toolLower == "run":
				related = true
			case strings.Contains(taskLower, "правка") && (toolLower == "searchreplace" || toolLower == "writefiles"):
				related = true
			case strings.Contains(taskLower, "поиск") && (toolLower == "grep" || toolLower == "glob" || toolLower == "codeseach"):
				related = true
			case strings.Contains(taskLower, "чит") && (toolLower == "readfiles" || toolLower == "readmap"):
				related = true
			case strings.Contains(taskLower, "структур") && toolLower == "structure":
				related = true
			case strings.Contains(taskLower, "git") && (toolLower == "git" || toolLower == "gitcommit"):
				related = true
			}

			if related {
				t.UpdateStatus(t.items[i].Name, status, toolName)
			}
		}
	}
}

func canonicalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Убираем артикли и числа в начале.
	s = regexp.MustCompile(`^\d+[.)\s]+`).ReplaceAllString(s, "")
	return s
}
