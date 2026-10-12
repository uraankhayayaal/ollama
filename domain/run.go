package domain

import "time"

type Usage struct {
	InputTokens  int
	OutputTokens int
	OutputTPS    float64
}

type ModelReply struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        *Usage
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolName   string
	ToolCallID string
}

type AgentResponse struct {
	Content    string
	ToolCalls  []ToolCall
	Truncated  bool
	Looped     bool
	LoopReason string
	Messages   []Message
	Rounds     int
}

type RoundState struct {
	Round         int
	Agent         string
	State         string
	VerifyRan     bool
	VerifyFailed  bool
	VerifyCommand string
	VerifyOutput  string
	CommitSHA     string
	Committed     bool
	Touched       int
}

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
