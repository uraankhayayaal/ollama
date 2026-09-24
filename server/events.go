package server

// EventType — тип события проекта во внутренней шине сессии (Ф-1,
// PLAN-done-dashboard-events). Вместо разрозненных «кто-то позвал kickBoard»
// компоненты эмитят события с типом и причиной; потребители (boardFlusher,
// а в Ф-2 — внутренние слушатели) сами решают, что делать.
type EventType string

const (
	// EventBoardChanged — доска могла измениться (эпики/задачи/баги/
	// git-состояние): подписчики опубликуют свежий снимок. Эмитится вместо
	// «kickBoard», чтобы у публикации была причина (Detail).
	EventBoardChanged EventType = "board_changed"

	// EventChatUpdated — чат проекта пополнился (новое сообщение).
	EventChatUpdated EventType = "chat_updated"

	// EventGitStatusChanged — изменился git-статус (ветки/MR) проекта.
	EventGitStatusChanged EventType = "git_status_changed"

	// EventTokensUpdated — обновились накопленные счётчики токенов проекта.
	EventTokensUpdated EventType = "tokens_updated"

	// EventSessionStatus — изменился статус сессии (running/standby/waiting/...).
	EventSessionStatus EventType = "session_status"
)

// ProjectEvent — событие проекта во внутренней шине сессии. Несёт тип и
// опциональную причину/данные (например, Detail — причина standby).
type ProjectEvent struct {
	Type   EventType
	Detail string
}

// Listener — подписчик на события проекта во внутренней шине (Ф-2).
// Вызывается синхронно из emit в контексте эмиттера — подписчик должен быть
// быстрым: без блокировок, долгих IO и вызовов emit (иначе рекурсия).
type Listener func(ProjectEvent)