package injections

import (
	"fmt"
	"strings"
	"testing"

	"ai/board"
)

func testRenderFn(content string, _ MergeContext) string { return content }
func testEvalFn(when string, _ MergeContext) bool       { return when == "true" }

func TestApplyInjections_Empty(t *testing.T) {
	result, err := ApplyInjections("base system", []Message{{Role: "user", Content: "hello"}}, nil, MergeContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.System != "base system" {
		t.Errorf("system = %q, want %q", result.System, "base system")
	}
	if len(result.Messages) != 1 {
		t.Errorf("messages len = %d, want 1", len(result.Messages))
	}
}

func TestApplyInjections_SystemPrepend(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "prepend", Content: "INJECTED"},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result.System, "INJECTED") {
		t.Errorf("system = %q, want prefix INJECTED", result.System)
	}
	if !strings.Contains(result.System, "base") {
		t.Errorf("system = %q, want to contain base", result.System)
	}
}

func TestApplyInjections_SystemAppend(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "append", Content: "INJECTED"},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(result.System, "INJECTED") {
		t.Errorf("system = %q, want suffix INJECTED", result.System)
	}
	if !strings.Contains(result.System, "base") {
		t.Errorf("system = %q, want to contain base", result.System)
	}
}

func TestApplyInjections_SystemReplace(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "replace", Content: "REPLACED"},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.System != "REPLACED" {
		t.Errorf("system = %q, want REPLACED", result.System)
	}
}

func TestApplyInjections_ScopeOrder(t *testing.T) {
	injs := []board.Injection{
		{Name: "runtime", Scope: "runtime", Target: "system", Position: "append", Content: "R"},
		{Name: "session", Scope: "session", Target: "system", Position: "append", Content: "S"},
		{Name: "assistant", Scope: "assistant", Target: "system", Position: "append", Content: "A"},
		{Name: "global", Scope: "global", Target: "system", Position: "append", Content: "G"},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gIdx := strings.Index(result.System, "G")
	sIdx := strings.Index(result.System, "S")
	aIdx := strings.Index(result.System, "A")
	rIdx := strings.Index(result.System, "R")
	if gIdx < 0 || sIdx < 0 || aIdx < 0 || rIdx < 0 {
		t.Fatalf("missing markers: G=%d S=%d A=%d R=%d in %q", gIdx, sIdx, aIdx, rIdx, result.System)
	}
	if !(gIdx < aIdx && aIdx < sIdx && sIdx < rIdx) {
		t.Errorf("wrong order: G=%d A=%d S=%d R=%d, want G<A<S<R", gIdx, aIdx, sIdx, rIdx)
	}
}

func TestApplyInjections_PriorityOrder(t *testing.T) {
	injs := []board.Injection{
		{Name: "low", Scope: "global", Target: "system", Position: "append", Content: "LOW", Priority: 1},
		{Name: "high", Scope: "global", Target: "system", Position: "append", Content: "HIGH", Priority: 10},
		{Name: "mid", Scope: "global", Target: "system", Position: "append", Content: "MID", Priority: 5},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hIdx := strings.Index(result.System, "HIGH")
	mIdx := strings.Index(result.System, "MID")
	lIdx := strings.Index(result.System, "LOW")
	if hIdx < 0 || mIdx < 0 || lIdx < 0 {
		t.Fatalf("missing markers: HIGH=%d MID=%d LOW=%d in %q", hIdx, mIdx, lIdx, result.System)
	}
	if !(hIdx < mIdx && mIdx < lIdx) {
		t.Errorf("wrong priority order: HIGH=%d MID=%d LOW=%d, want HIGH<MID<LOW", hIdx, mIdx, lIdx)
	}
}

func TestApplyInjections_MessagesPrepend(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "messages", Position: "prepend", Content: "INJECTED"},
	}
	baseMsgs := []Message{{Role: "user", Content: "hello"}}
	result, err := ApplyInjections("base", baseMsgs, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(result.Messages))
	}
	if result.Messages[0].Content != "INJECTED" {
		t.Errorf("first message = %q, want INJECTED", result.Messages[0].Content)
	}
	if result.Messages[1].Content != "hello" {
		t.Errorf("second message = %q, want hello", result.Messages[1].Content)
	}
}

func TestApplyInjections_MessagesAppend(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "messages", Position: "append", Content: "INJECTED"},
	}
	baseMsgs := []Message{{Role: "user", Content: "hello"}}
	result, err := ApplyInjections("base", baseMsgs, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(result.Messages))
	}
	if result.Messages[1].Content != "INJECTED" {
		t.Errorf("last message = %q, want INJECTED", result.Messages[1].Content)
	}
}

func TestApplyInjections_MessagesReplace(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "messages", Position: "replace", Content: "REPLACED"},
	}
	baseMsgs := []Message{{Role: "user", Content: "hello"}}
	result, err := ApplyInjections("base", baseMsgs, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(result.Messages))
	}
	if result.Messages[0].Content != "REPLACED" {
		t.Errorf("message = %q, want REPLACED", result.Messages[0].Content)
	}
}

func TestApplyInjections_MessagesInjectAtIndex(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "messages", Position: "inject_at_index", Index: 1, Content: "INJECTED"},
	}
	baseMsgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
	}
	result, err := ApplyInjections("base", baseMsgs, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(result.Messages))
	}
	if result.Messages[1].Content != "INJECTED" {
		t.Errorf("message[1] = %q, want INJECTED", result.Messages[1].Content)
	}
}

func TestApplyInjections_UserLastPrepend(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "user_last", Position: "prepend", Content: "HINT"},
	}
	baseMsgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "world"},
	}
	result, err := ApplyInjections("base", baseMsgs, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Messages) != 4 {
		t.Fatalf("messages len = %d, want 4", len(result.Messages))
	}
	lastUser := result.Messages[3]
	if !strings.HasPrefix(lastUser.Content, "HINT") {
		t.Errorf("last user message = %q, want prefix HINT", lastUser.Content)
	}
	if !strings.Contains(lastUser.Content, "world") {
		t.Errorf("last user message = %q, want to contain world", lastUser.Content)
	}
}

func TestApplyInjections_UserLastAppend(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "user_last", Position: "append", Content: "HINT"},
	}
	baseMsgs := []Message{
		{Role: "user", Content: "hello"},
	}
	result, err := ApplyInjections("base", baseMsgs, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(result.Messages[0].Content, "HINT") {
		t.Errorf("user message = %q, want suffix HINT", result.Messages[0].Content)
	}
}

func TestApplyInjections_AssistantLastPrepend(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "assistant_last", Position: "prepend", Content: "HINT"},
	}
	baseMsgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	result, err := ApplyInjections("base", baseMsgs, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result.Messages[1].Content, "HINT") {
		t.Errorf("assistant message = %q, want prefix HINT", result.Messages[1].Content)
	}
}

func TestApplyInjections_DisabledSkipped(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "append", Content: "INJECTED", Disabled: true},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.System != "base" {
		t.Errorf("system = %q, want base (disabled injection skipped)", result.System)
	}
}

func TestApplyInjections_WhenCondition(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "append", Content: "INJECTED", When: "true"},
	}
	ctx := MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn}
	result, err := ApplyInjections("base", nil, injs, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.System, "INJECTED") {
		t.Errorf("system = %q, want to contain INJECTED", result.System)
	}
}

func TestApplyInjections_WhenConditionFalse(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "append", Content: "INJECTED", When: "false"},
	}
	ctx := MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn}
	result, err := ApplyInjections("base", nil, injs, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.System != "base" {
		t.Errorf("system = %q, want base (condition false)", result.System)
	}
}

func TestApplyInjections_TemplateVars(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "append", Content: "Hello {{vars.name}}!"},
	}
	renderFn := func(content string, ctx MergeContext) string {
		if v, ok := ctx.Vars["name"]; ok {
			return strings.ReplaceAll(content, "{{vars.name}}", fmt.Sprintf("%v", v))
		}
		return content
	}
	ctx := MergeContext{Vars: map[string]any{"name": "World"}, RenderFn: renderFn, EvalFn: testEvalFn}
	result, err := ApplyInjections("base", nil, injs, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.System, "Hello World!") {
		t.Errorf("system = %q, want to contain 'Hello World!'", result.System)
	}
}

func TestApplyInjections_ContentTruncation(t *testing.T) {
	longContent := strings.Repeat("x", MaxContentSize+100)
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "append", Content: longContent},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.System, "[truncated]") {
		t.Errorf("system = %q, want to contain [truncated]", result.System)
	}
}

func TestApplyInjections_TotalSizeLimit(t *testing.T) {
	contentSize := MaxContentSize + 100 // slightly over limit so truncation triggers
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "append", Content: strings.Repeat("a", contentSize)},
		{Name: "inj2", Scope: "global", Target: "system", Position: "append", Content: strings.Repeat("b", contentSize)},
		{Name: "inj3", Scope: "global", Target: "system", Position: "append", Content: strings.Repeat("c", contentSize)},
		{Name: "inj4", Scope: "global", Target: "system", Position: "append", Content: strings.Repeat("d", contentSize)},
		{Name: "inj5", Scope: "global", Target: "system", Position: "append", Content: strings.Repeat("e", contentSize)},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ReplaceWarning == "" {
		t.Errorf("expected ReplaceWarning for total size limit")
	}
}

func TestApplyInjections_AppliedMetadata(t *testing.T) {
	injs := []board.Injection{
		{ID: "inj-1", Name: "Test", Scope: "global", Target: "system", Position: "append", Content: "X"},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Applied) != 1 {
		t.Fatalf("applied len = %d, want 1", len(result.Applied))
	}
	if result.Applied[0].ID != "inj-1" {
		t.Errorf("applied[0].ID = %q, want inj-1", result.Applied[0].ID)
	}
	if result.Applied[0].Name != "Test" {
		t.Errorf("applied[0].Name = %q, want Test", result.Applied[0].Name)
	}
}

func TestCollectInjections_Order(t *testing.T) {
	global := []board.Injection{{Name: "g"}}
	assistant := []board.Injection{{Name: "a"}}
	session := []board.Injection{{Name: "s"}}
	runtime := []board.Injection{{Name: "r"}}

	result := CollectInjections(global, assistant, session, runtime)
	if len(result) != 4 {
		t.Fatalf("len = %d, want 4", len(result))
	}
	if result[0].Name != "g" || result[1].Name != "a" || result[2].Name != "s" || result[3].Name != "r" {
		t.Errorf("wrong order: %s, %s, %s, %s", result[0].Name, result[1].Name, result[2].Name, result[3].Name)
	}
}

func TestDeduplicateByID(t *testing.T) {
	injs := []board.Injection{
		{ID: "1", Name: "first", Content: "A"},
		{ID: "2", Name: "second", Content: "B"},
		{ID: "1", Name: "third", Content: "C"},
	}
	result := DeduplicateByID(injs)
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2", len(result))
	}
	if result[0].Name != "third" {
		t.Errorf("result[0].Name = %q, want third (last wins)", result[0].Name)
	}
	if result[1].Name != "second" {
		t.Errorf("result[1].Name = %q, want second", result[1].Name)
	}
}
