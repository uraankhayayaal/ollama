package chatassist

import (
	"strings"

	appboard "ai/application/board"
	"ai/domain"
)

const (
	// DialogueMaxTurns — сколько последних реплик передаём модели.
	DialogueMaxTurns = 12
	// DialogueMsgLen — лимит символов на одну реплику.
	DialogueMsgLen = 400
	// DialogueHistoryN — столько читаем из Redis (запас на фильтрацию).
	DialogueHistoryN = 40
)

// OneLine сводит многострочный текст в одну строку (лимит символов).
func OneLine(s string, n int) string {
	return strings.ReplaceAll(appboard.TruncateText(s, n), "\n", " ")
}

// DialogueHistory форматирует последние реплики диалога в строки промпта.
// Порядок в messages хронологический (старые → новые), последняя запись —
// только что добавленное сообщение пользователя. Пропускаем её.
func DialogueHistory(messages []domain.ChatMessage) string {
	if len(messages) == 0 {
		return ""
	}

	start := 0
	if messages[len(messages)-1].Role == domain.ChatRoleUser {
		start = 1
	}

	var lines []string
	for i := len(messages) - 1 - start; i >= 0; i-- {
		if len(lines) >= DialogueMaxTurns {
			break
		}
		m := messages[i]
		switch m.Role {
		case domain.ChatRoleUser, domain.ChatRoleAssistant:
			if txt := strings.TrimSpace(m.Content); txt != "" {
				speaker := "пользователь"
				if m.Role == domain.ChatRoleAssistant {
					speaker = "ассистент"
				}
				lines = append(lines, "["+speaker+"] "+OneLine(txt, DialogueMsgLen))
			}
		case domain.ChatRoleAsk:
			if txt := AskSummary(m.Ask); txt != "" {
				lines = append(lines, "[ассистент] "+OneLine(txt, DialogueMsgLen))
			}
		case domain.ChatRoleTool:
			if txt := strings.TrimSpace(m.Content); txt != "" {
				spyateli := "инструмент"
				if m.Tool != "" {
					spyateli = m.Tool + " →"
				}
				lines = append(lines, "["+spyateli+"] "+OneLine(txt, DialogueMsgLen))
			}
		}
	}

	// Собирали свежие → старые; разворачиваем в хронологический порядок.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}
