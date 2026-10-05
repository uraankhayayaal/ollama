package injections

import (
	"fmt"
	"strings"
	"testing"

	"ai/board"
)

func testRenderFn(content string, _ MergeContext) string { return content }
func testEvalFn(when string, _ MergeContext) bool        { return when == "true" }

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
	off := false
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "append", Content: "INJECTED", Enabled: &off},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.System != "base" {
		t.Errorf("system = %q, want base (disabled injection skipped)", result.System)
	}
	if len(result.Applied) != 0 {
		t.Errorf("applied = %d, want 0", len(result.Applied))
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Name != "inj1" {
		t.Errorf("skipped = %+v, want запись inj1", result.Skipped)
	}
}

// Запись без поля enabled активна: обратная совместимость (старые записи
// доски, где фла не было вовсе).
func TestApplyInjections_EnabledDefaultsTrue(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "append", Content: "INJECTED"},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.System, "INJECTED") {
		t.Errorf("system = %q, want INJECTED (nil enabled = активна)", result.System)
	}
}

// Некорректная запись (её могли принести правкой доски вручную) не должна
// попасть в промпт молча: она уезжает в Skipped с причиной.
func TestApplyInjections_InvalidSkipped(t *testing.T) {
	injs := []board.Injection{
		{Name: "bad-target", Scope: "global", Target: "before_tools", Position: "append", Content: "X"},
		{Name: "no-content", Scope: "global", Target: "system", Position: "append"},
		{Name: "bad-scope", Scope: "мистика", Target: "system", Position: "append", Content: "Y"},
		{Name: "bad-position", Scope: "global", Target: "system", Position: "куда-то", Content: "Z"},
		{Name: "neg-index", Scope: "global", Target: "system", Position: "inject_at_index", Index: -1, Content: "W"},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.System != "base" {
		t.Errorf("system = %q, want base (все записи невалидны)", result.System)
	}
	if len(result.Applied) != 0 {
		t.Errorf("applied = %d, want 0", len(result.Applied))
	}
	if len(result.Skipped) != 5 {
		t.Fatalf("skipped = %d, want 5: %+v", len(result.Skipped), result.Skipped)
	}
	for _, s := range result.Skipped {
		if s.Reason == "" {
			t.Errorf("пустая причина пропуска для %q", s.Name)
		}
	}
}

// Один id в двух источниках — перекрытие: применяется последняя запись.
func TestApplyInjections_DedupAcrossSources(t *testing.T) {
	off := false
	injs := CollectInjections(
		[]board.Injection{{ID: "x", Name: "из global", Scope: "global", Target: "system", Position: "append", Content: "OLD", Enabled: &off}},
		nil,
		nil,
		[]board.Injection{{ID: "x", Name: "из runtime", Scope: "runtime", Target: "system", Position: "append", Content: "NEW"}},
	)
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.System, "OLD") || !strings.Contains(result.System, "NEW") {
		t.Errorf("system = %q, want только NEW (last-wins по id)", result.System)
	}
	if len(result.Applied) != 1 || result.Applied[0].Name != "из runtime" {
		t.Errorf("applied = %+v, want одна запись «из runtime»", result.Applied)
	}
}

// Сообщения, добавленные инъекцией в середину истории, должны иметь роль
// user: системный блок на середине диалога ломает некоторые провайдеры.
func TestApplyInjections_MessagesUseUserRole(t *testing.T) {
	injs := []board.Injection{
		{Name: "m1", Scope: "runtime", Target: "messages", Position: "append", Content: "ХОД"},
	}
	base := []Message{{Role: "user", Content: "задача"}}
	result, err := ApplyInjections("sys", base, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	last := result.Messages[len(result.Messages)-1]
	if last.Role != "user" || last.Content != "ХОД" {
		t.Errorf("последнее сообщение = %+v, want {user ХОД}", last)
	}
}

// Пустой системный промпт: prepend/append не оставляют «висячих» переводов
// строк, чтобы не начинать сообщение с пустых строк.
func TestApplyInjections_EmptySystemNoSeparators(t *testing.T) {
	for _, pos := range []string{"prepend", "append"} {
		injs := []board.Injection{
			{Name: "s", Scope: "runtime", Target: "system", Position: pos, Content: "INJECTED"},
		}
		result, err := ApplyInjections("", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", pos, err)
		}
		if result.System != "INJECTED" {
			t.Errorf("%s: system = %q, want INJECTED", pos, result.System)
		}
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

// Обрезка на применении: запись прошла валидацию, но ШАБЛОН раздул её за
// лимит (например, {{env.X}} вставил мегабайт). Такое тоже режется.
func TestApplyInjections_ContentTruncationAfterRender(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "append", Content: "начало"},
	}
	// Рендерер раздувает контент за лимит одной инъекции.
	blowUp := func(content string, _ MergeContext) string { return strings.Repeat("x", MaxContentSize+100) }
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: blowUp, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.System, "[truncated]") {
		t.Errorf("system должен содержать [truncated], got %d байт", len(result.System))
	}
}

// Запись БОЛЬШЕ лимита отсекается валидацией (на записи её бы отвергли) —
// в промпт мусор не попадает.
func TestApplyInjections_OversizedRejected(t *testing.T) {
	injs := []board.Injection{
		{Name: "inj1", Scope: "global", Target: "system", Position: "append", Content: strings.Repeat("x", MaxContentSize+100)},
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.System != "base" {
		t.Errorf("system = %q, want base", result.System)
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("skipped = %d, want 1: %+v", len(result.Skipped), result.Skipped)
	}
}

func TestApplyInjections_TotalSizeLimit(t *testing.T) {
	// Каждая запись в пределах лимита, но вместе превышают MaxTotalSize:
	// лимит суммы проверяется на применении, а не валидацией записи.
	chunk := strings.Repeat("a", MaxContentSize)
	var injs []board.Injection
	for i := 0; i < 6; i++ { // 6 × 10240 = 61440 > 51200
		injs = append(injs, board.Injection{
			Name: fmt.Sprintf("inj%d", i), Scope: "global", Target: "system",
			Position: "append", Content: chunk,
		})
	}
	result, err := ApplyInjections("base", nil, injs, MergeContext{RenderFn: testRenderFn, EvalFn: testEvalFn})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ReplaceWarning == "" {
		t.Errorf("expected ReplaceWarning for total size limit")
	}
	// Часть инъекций применилась, остальные отброшены по лимиту суммы.
	if len(result.Applied) == 0 || len(result.Applied) >= len(injs) {
		t.Errorf("applied = %d, хотим часть из %d", len(result.Applied), len(injs))
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
