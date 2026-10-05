package injections

import (
	"strings"
	"testing"

	"ai/board"
)

// TestApplyInjections_Integration проверяет полный пайплайн:
// collect → filter → sort → apply с инъекциями из всех источников.
func TestApplyInjections_Integration(t *testing.T) {
	globalInjs := []board.Injection{
		{Name: "g-1", Scope: "global", Target: "system", Position: "append", Content: "global-inj"},
	}
	assistantInjs := []board.Injection{
		{Name: "a-1", Scope: "assistant", Target: "system", Position: "append", Content: "assistant-inj"},
	}
	sessionInjs := []board.Injection{
		{Name: "s-1", Scope: "session", Target: "system", Position: "append", Content: "session-inj"},
	}
	runtimeInjs := []board.Injection{
		{Name: "r-1", Scope: "runtime", Target: "system", Position: "append", Content: "runtime-inj"},
	}

	allInjs := CollectInjections(globalInjs, assistantInjs, sessionInjs, runtimeInjs)
	if len(allInjs) != 4 {
		t.Fatalf("expected 4 injections, got %d", len(allInjs))
	}

	result, err := ApplyInjections("base", nil, allInjs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Проверка: все инъекции были применены
	if len(result.Applied) != 4 {
		t.Fatalf("expected 4 applied injections, got %d", len(result.Applied))
	}

	// Проверка порядка: global → assistant → session → runtime (append)
	// Конечная строка должна содержать все инъекции
	for _, name := range []string{"global-inj", "assistant-inj", "session-inj", "runtime-inj"} {
		if !strings.Contains(result.System, name) {
			t.Errorf("expected %q in system, got %q", name, result.System)
		}
	}
}

// TestApplyInjections_EmptyFastPath проверяет fast-path без инъекций.
func TestApplyInjections_EmptyFastPath(t *testing.T) {
	result, err := ApplyInjections("base", []Message{{Role: "user", Content: "hi"}}, nil, MergeContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.System != "base" {
		t.Errorf("expected base system, got %q", result.System)
	}
	if len(result.Messages) != 1 || result.Messages[0].Content != "hi" {
		t.Errorf("expected unchanged messages, got %+v", result.Messages)
	}
	if len(result.Applied) != 0 {
		t.Errorf("expected no applied injections, got %d", len(result.Applied))
	}
}

func TestCollectInjections_MixesSources(t *testing.T) {
	global := []board.Injection{{ID: "g1", Name: "G", Scope: "global"}}
	assistant := []board.Injection{{ID: "a1", Name: "A", Scope: "assistant"}}
	session := []board.Injection{{ID: "s1", Name: "S", Scope: "session"}}
	runtime := []board.Injection{{ID: "r1", Name: "R", Scope: "runtime"}}

	all := CollectInjections(global, assistant, session, runtime)
	if len(all) != 4 {
		t.Fatalf("expected 4, got %d", len(all))
	}
	if all[0].ID != "g1" || all[1].ID != "a1" || all[2].ID != "s1" || all[3].ID != "r1" {
		t.Fatalf("wrong collection order: %v", all)
	}
}