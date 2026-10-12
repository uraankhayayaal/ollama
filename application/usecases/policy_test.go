package usecases

import (
	"strings"
	"testing"
)

func TestNeedContextTargets(t *testing.T) {
	content := "Нужно дочитать.\nNEED_CONTEXT: internal/service/a.go:10-20\n" +
		"NEED_SIGNATURE b.go\nNEED_FILE: c.go\n" +
		"NEED_CONTEXT: internal/service/a.go:10-20\nбез маркеров"
	want := []string{"internal/service/a.go:10-20", "b.go", "c.go"}
	got := NeedContextTargets(content)
	if len(got) != len(want) {
		t.Fatalf("NeedContextTargets = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NeedContextTargets[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got := NeedContextTargets("без маркеров"); len(got) != 0 {
		t.Fatalf("NeedContextTargets(без маркеров) = %#v, want пусто", got)
	}
}

type staticFetcher map[string]string

func (f staticFetcher) FetchContext(target string) (string, bool) {
	txt, ok := f[target]
	return txt, ok
}

func TestNeedContextPull(t *testing.T) {
	agent := staticFetcher{"a.go": "AAA"}
	pulled, ok := NeedContextPull(agent, "NEED_SIGNATURE a.go\nNEED_FILE missing.go")
	if !ok {
		t.Fatal("ожидали хотя бы одну цель")
	}
	if pulled["a.go"] != "AAA" {
		t.Fatalf("pulled = %#v", pulled)
	}
	if _, ok := pulled["missing.go"]; ok {
		t.Fatalf("нераспознанная цель не должна попасть: %#v", pulled)
	}

	if _, ok := NeedContextPull(struct{}{}, "NEED_FILE a.go"); ok {
		t.Fatal("агент без ContextFetcher не должен давать дозаправку")
	}
	if _, ok := NeedContextPull(agent, "текст без маркеров"); ok {
		t.Fatal("без маркеров дозаправки нет")
	}
}

func TestToolResultFailed(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		result   string
		wantFail bool
	}{
		{"error пишущий", "WriteFiles", `{"status":"error"}`, true},
		{"error чтение", "ReadFiles", `{"status":"error"}`, false},
		{"error List", "List", `{"status":"error"}`, false},
		{"empty пишущий", "WriteFiles", `{"status":"empty"}`, true},
		{"empty List", "List", `{"status":"empty"}`, false},
		{"ok", "WriteFiles", `{"status":"ok"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolResultFailed(tc.tool, []byte(tc.result)); got != tc.wantFail {
				t.Fatalf("ToolResultFailed(%q, %s) = %v, want %v", tc.tool, tc.result, got, tc.wantFail)
			}
		})
	}
}

func TestToolFailMessageMentionsRequired(t *testing.T) {
	if msg := ToolFailMessage("ReadFiles", 5, ""); strings.Contains(msg, "Обязательное действие") {
		t.Fatalf("без required подсказка не должна упоминать обязательное действие: %q", msg)
	}
	msg := ToolFailMessage("ReadFiles", 5, "WriteFiles")
	if !strings.Contains(msg, "WriteFiles") {
		t.Fatalf("подсказка с required должна называть инструмент: %q", msg)
	}
}
