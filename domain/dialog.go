package domain

import "time"

// Типы диалога Web UI (Этап 8, H5b): данные, общие для application-слоя
// (разбор структурированного вопроса, сводка вопросов, форматирование истории)
// и инфраструктурного хранилища диалога (chat.Store). Имена с префиксом Chat
// для ролей/сообщений — чтобы не сталкиваться с domain.Role (роль агента) и
// domain.Message (нейтральное сообщение провайдера).

// ChatRole — роль автора сообщения в диалоге.
type ChatRole string

const (
	ChatRoleUser      ChatRole = "user"      // сообщение пользователя
	ChatRoleAssistant ChatRole = "assistant" // текст модели
	ChatRoleTool      ChatRole = "tool"      // результат инструмента
	ChatRoleSystem    ChatRole = "system"    // служебное (начало сессии и т.п.)
	ChatRoleStatus    ChatRole = "status"    // статусная строка (агент начал/закончил работу)
	ChatRoleBoard     ChatRole = "board"     // событие доски / HITL-затвор
	ChatRoleAsk       ChatRole = "ask"       // структурированный вопрос ассистента пользователю
)

// ChatMessage — единица диалога. ID заполняется из ID записи Redis Stream при
// чтении истории; при Append может быть пустым.
type ChatMessage struct {
	ID      string    `json:"id,omitempty"`
	Role    ChatRole  `json:"role"`
	Content string    `json:"content"`
	Agent   string    `json:"agent,omitempty"` // имя агента (для assistant/tool)
	Tool    string    `json:"tool,omitempty"`  // имя инструмента (для role=tool)
	OK      *bool     `json:"ok,omitempty"`    // успешен ли результат инструмента
	Ask     *Ask      `json:"ask,omitempty"`   // структурированный вопрос (для role=ask)
	Time    time.Time `json:"time"`
}

// AskKind — тип структурированного вопроса: одиночный выбор (single) или
// множественный (multi). Каждый вопрос дополняется вариантами ответа и
// необязательным кастомным вариантом с полем ввода (план «спроси пользователя
// при неоднозначности»).
type AskKind string

const (
	AskSingle AskKind = "single" // клик по одному варианту = ответ
	AskMulti  AskKind = "multi"  // несколько чекбоксов + кнопка «Подтвердить»
)

// Ask — структурированный вопрос ассистента (инструмент AskUser): пачка
// вопросов, которая показывается пользователю пошагово в чате. Frontend
// рендерит карточку-вардин: один вопрос на шаг, кастомный вариант с полем
// ввода, выделение предпочтительного ответа (recommended).
type Ask struct {
	ID        string        `json:"id,omitempty"` // id пачки вопросов (для REST-ответа)
	Questions []AskQuestion `json:"questions"`
}

// AskQuestion — один шаг вардина.
type AskQuestion struct {
	ID   string  `json:"id"`
	Text string  `json:"text"`
	Kind AskKind `json:"kind"` // single | multi
	// AllowCustom — показывать ли вариант «Свой ответ» с полем ввода
	// (по умолчанию true, если не указано).
	AllowCustom bool        `json:"allow_custom,omitempty"`
	Options     []AskOption `json:"options"`
}

// AskOption — вариант ответа. Recommended — предпочтительный вариант: в UI
// выделен бейджем «рекомендую», но решение остаётся за пользователем.
type AskOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Recommended bool   `json:"recommended,omitempty"`
}

// AskAnswer — ответ пользователя на один вопрос (возвращается модели как
// результат инструмента AskUser).
type AskAnswer struct {
	QuestionID string   `json:"question_id"`
	Selected   []string `json:"selected"`         // выбранные id вариантов
	Custom     string   `json:"custom,omitempty"` // текст кастомного ответа (если выбран)
}
