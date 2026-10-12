package domain

type StreamChunk struct {
	Partial string
	Done    bool
}

type ModelLimits struct {
	InputTokens  int
	OutputTokens int
	ThinkTokens  int
}
