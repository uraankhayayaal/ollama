package domain

import (
	"fmt"
	"time"
)

// ToolCall — запрос модели на вызов инструмента (сырые аргументы — JSON-строка).
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON-строка с аргументами
}

// Usage — фактическое потребление токенов одного запроса к модели.
type Usage struct {
	// InputTokens — токены, отправленные модели на вход (вся история).
	InputTokens int
	// OutputTokens — токены, сгенерированные моделью в ответ.
	OutputTokens int
	// OutputTPS — реальная скорость генерации (вых. ток/с), когда провайдер
	// её сообщает (Ollama eval_count/eval_duration); 0, если неизвестна.
	OutputTPS float64
}

// ModelReply — ответ модели за один раунд.
type ModelReply struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	// Usage — фактическое потребление токенов раунда, если провайдер его
	// отдаёт (Ollama eval_count/prompt_eval_count, OpenAI-совместимые usage).
	// nil — провайдер не сообщил; раннер считает эвристическую оценку.
	Usage *Usage
}

// Message — нейтральное представление сообщения диалога,
// провайдеры конвертируют его в свой формат.
type Message struct {
	Role       string // "system" | "user" | "assistant" | "tool"
	Content    string
	ToolCalls  []ToolCall // для assistant
	ToolName   string     // для tool
	ToolCallID string     // для tool
}

// AgentResponse содержит ответ модели и все выполненные вызовы функций.
type AgentResponse struct {
	Content   string
	ToolCalls []ToolCall
	// Truncated указывает, что цикл остановлен по лимиту раундов (maxRounds)
	// или модель обрезалась по лимиту токенов, а не потому, что модель
	// завершила ответ корректно.
	Truncated bool
	// Looped указывает, что цикл прерван ДЕТЕКТОРОМ ЗАЦИКЛИВАНИЯ: модель
	// повторяла один и тот же вызов (или не делала ничего нового), не поняла
	// свою ошибку и не предприняла других действий даже после разрыва петли.
	// В отличие от Truncated это не «бюджет кончился», а приговор: продолжать
	// тот же диалог бессмысленно.
	Looped bool
	// LoopReason — человекочитаемая причина зацикливания (для лога, ошибки
	// оркестратора и интерфейса).
	LoopReason string
	// Messages — полная история диалога цикла (system/user + все раунды).
	// Сохраняется вызывающим кодом в чекпоинт для возобновления (resume).
	Messages []Message
	// Rounds — количество уже потраченных раундов цикла. При resume новый
	// запуск продолжит с раунда Rounds+1, получив снова полный бюджет maxRounds.
	Rounds int
}

// StopReason возвращает причину, по которой цикл не завершился успешно:
// «зацикливание», «лимит раундов» или пусто (модель закончила сама).
func (r *AgentResponse) StopReason() string {
	if r == nil {
		return ""
	}
	switch {
	case r.Looped:
		return "зацикливание"
	case r.Truncated:
		return "лимит раундов"
	}
	return ""
}

// LoopError возвращает ошибку для вызывающего кода, если цикл прерван
// зацикливанием (nil иначе). Вызывающий код обязан вернуть её наружу: задача,
// на которой агент зациклился, не выполнена, и тихий fallback «готово» здесь
// означал бы потерю работы.
func (r *AgentResponse) LoopError(prefix string) error {
	if r == nil || !r.Looped {
		return nil
	}
	return fmt.Errorf("%s: агент зациклился и не предпринял других действий (%s) — задача не выполнена", prefix, r.LoopReason)
}

// RoundState — состояние задачи по итогам одного раунда агента.
type RoundState struct {
	// Round — номер раунда (1-based), для журнала.
	Round int
	// Agent — роль/имя специалиста, который делал раунд. Раннер агента по
	// имени не знает (в agents.Agent имени нет), поэтому поле заполняет сам
	// репортёр.
	Agent string
	// State — agent_state: writing_code/running_tests/fixing_errors/idle.
	State string
	// VerifyRan/VerifyFailed — была ли в раунде проверка и падала ли она.
	VerifyRan    bool
	VerifyFailed bool
	// VerifyCommand — нормализованная команда последней проверки раунда.
	VerifyCommand string
	// VerifyOutput — усечённый вывод последней УПАВШЕЙ проверки раунда.
	VerifyOutput string
	// CommitSHA/Committed — коммит промежуточной фиксации раунда.
	CommitSHA string
	Committed bool
	// Touched — сколько файлов изменено в раунде.
	Touched int
}

// Run — один запуск агента по задаче.
type Run struct {
	ID          string
	Project     string
	TaskID      string
	EpicID      string
	AgentRole   AgentRole
	Status      Status
	StartTime   time.Time
	EndTime     time.Time
	RoundStates []RoundState
}
