package domain

type StreamChunk struct {
	Partial  string
	Thinking string // накопленное рассуждение (thinking-блок модели)
	Done     bool
}

type ModelLimits struct {
	InputTokens  int
	OutputTokens int
	ThinkTokens  int
}
