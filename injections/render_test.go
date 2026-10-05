package injections

import (
	"strings"
	"testing"

	"ai/board"
)

// Раньше подстановка {{env.*}} отдавала ЛЮБУЮ переменную окружения: проверено
// было, что {{env.AWS_SECRET_ACCESS_KEY}}, {{env.MY_PRIVATE_KEY}} и
// {{env.PATH}} возвращали настоящие значения. Теперь окружение закрыто
// allowlist'ом: подставляется только явно разрешённое.
func TestRenderEnvAllowlist(t *testing.T) {
	t.Setenv("AI_TEST_TOKEN_OK", "разрешённое-значение")
	t.Setenv("CODEGEN_TEST_VALUE", "разрешённое-значение-2")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "super-secret")
	t.Setenv("MY_PRIVATE_KEY", "-----BEGIN PRIVATE KEY-----")
	t.Setenv("HOME", "/home/кто-то")

	cases := []struct {
		name    string
		content string
		want    string
		denied  bool // значение НЕ должно появиться в результате
	}{
		{"allowlist AI_", "{{env.AI_TEST_TOKEN_OK}}", "разрешённое-значение", false},
		{"allowlist CODEGEN_", "{{env.CODEGEN_TEST_VALUE}}", "разрешённое-значение-2", false},
		{"секрет вне allowlist", "{{env.AWS_SECRET_ACCESS_KEY}}", "", true},
		{"приватный ключ вне allowlist", "{{env.MY_PRIVATE_KEY}}", "", true},
		{"PATH вне allowlist", "{{env.PATH}}", "", true},
		{"HOME вне allowlist", "{{env.HOME}}", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderFnDefault(tc.content, MergeContext{})
			if tc.denied {
				if strings.Contains(got, "super-secret") || strings.Contains(got, "PRIVATE KEY") {
					t.Errorf("утечка секрета в промпт: %q", got)
				}
				// Неизвестное/запрещённое остаётся плейсхолдером: молча
				// подставлять пустую строку опаснее обрыва фразы.
				if got != tc.content {
					t.Errorf("ожидался нетронутый плейсхолдер %q, получено %q", tc.content, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want содержать %q", got, tc.want)
			}
		})
	}
}

// Чувствительные имена запрещены даже внутри allowlist: префикс не должен
// обходить запрет.
func TestRenderEnvSensitiveAlwaysRedacted(t *testing.T) {
	t.Setenv("AI_SERVICE_API_KEY", "должен-быть-скрыт")
	t.Setenv("AI_ACCESS_TOKEN", "должен-быть-скрыт")
	t.Setenv("AI_DB_PASSWORD", "должен-быть-скрыт")

	for _, name := range []string{"AI_SERVICE_API_KEY", "AI_ACCESS_TOKEN", "AI_DB_PASSWORD"} {
		if EnvAllowed(name) {
			t.Errorf("EnvAllowed(%q) = true, want false (чувствительное имя)", name)
		}
	}
	got := RenderFnDefault("{{env.AI_SERVICE_API_KEY}}", MergeContext{})
	if strings.Contains(got, "должен-быть-скрыт") {
		t.Errorf("значение попало в промпт: %q", got)
	}
	if got != "[REDACTED]" {
		t.Errorf("got %q, want [REDACTED]", got)
	}
}

// Allowlist расширяется переменной AI_INJECTION_ENV_ALLOW.
func TestRenderEnvAllowlistExtendable(t *testing.T) {
	t.Setenv("MYAPP_STAGE", "prod")
	if EnvAllowed("MYAPP_STAGE") {
		t.Error("MYAPP_STAGE разрешён без AI_INJECTION_ENV_ALLOW")
	}
	t.Setenv("AI_INJECTION_ENV_ALLOW", "MYAPP_")
	if !EnvAllowed("MYAPP_STAGE") {
		t.Error("MYAPP_STAGE не разрешён после AI_INJECTION_ENV_ALLOW")
	}
	got := RenderFnDefault("окружение: {{env.MYAPP_STAGE}}", MergeContext{})
	if !strings.Contains(got, "prod") {
		t.Errorf("got %q, want содержать prod", got)
	}
}

// Значение из контекста важнее процесса: MergeContext.Env — явная передача.
func TestRenderEnvFromContext(t *testing.T) {
	got := RenderFnDefault("{{env.AI_FROM_CTX}}", MergeContext{Env: map[string]string{"AI_FROM_CTX": "из-контекста"}})
	if !strings.Contains(got, "из-контекста") {
		t.Errorf("got %q, want «из-контекста»", got)
	}
}

// Пространство assistant: раньше {{assistant.name}} не рендерилось вовсе.
func TestRenderAssistantNamespace(t *testing.T) {
	ctx := MergeContext{Assistant: map[string]any{"name": "Ассистент", "project": "mytrip"}}
	got := RenderFnDefault("{{assistant.name}} / {{assistant.project}}", ctx)
	if got != "Ассистент / mytrip" {
		t.Errorf("got %q, want «Ассистент / mytrip»", got)
	}
	// Неизвестный ключ остаётся плейсхолдером, а не пустой строкой.
	if got := RenderFnDefault("{{assistant.нет}}", ctx); got != "{{assistant.нет}}" {
		t.Errorf("got %q, want нетронутый плейсхолдер", got)
	}
}

func TestRenderVarsAndSession(t *testing.T) {
	ctx := MergeContext{
		Vars:    map[string]any{"task": "FEL-02"},
		Session: map[string]any{"project": "mytrip"},
	}
	if got := RenderFnDefault("задача {{vars.task}} в {{session.project}}", ctx); got != "задача FEL-02 в mytrip" {
		t.Errorf("got %q", got)
	}
}

// Условия when вычисляются по реальному контексту прогона.
func TestEvalFnDefaultWithMergeContext(t *testing.T) {
	ctx := MergeContext{
		Model: "qwen3:30b", Provider: "ollama", Role: "backend",
		Project: "mytrip", TaskID: "FEL-02", Turn: 3,
		User: "ivan", Tools: []string{"ReadFiles", "Run"}, HasFiles: true,
	}
	yes := []string{
		`model == "qwen3:30b"`,
		`provider == "ollama"`,
		`role == "backend"`,
		`project == "mytrip"`,
		`task_id == "FEL-02"`,
		`turn == "3"`,
		`turn > "2"`,
		`user == "ivan"`,
		`has_files == "true"`,
		`tools != ""`,
		`role == "backend" && project == "mytrip"`,
		`role == "frontend" || role == "backend"`,
		`!(role == "frontend")`,
		`(role == "frontend" || role == "backend") && has_files == "true"`,
	}
	no := []string{
		`model == "llama3"`,
		`project == "другой"`,
		`role == "frontend"`,
		`has_files == "false"`,
		`turn == "9"`,
		`project != "mytrip"`,
		`role == "backend" && project == "другой"`,
		`неизвестная_переменная == "x"`, // мусорное условие не должно «проходить»
	}
	for _, w := range yes {
		if !EvalFnDefault(w, ctx) {
			t.Errorf("when=%q ожидалось true", w)
		}
	}
	for _, w := range no {
		if EvalFnDefault(w, ctx) {
			t.Errorf("when=%q ожидалось false", w)
		}
	}
	// Пустое условие = применять всегда.
	if !EvalFnDefault("", ctx) {
		t.Error("пустое условие должно быть true")
	}
}

// Логи применения/пропуска содержат только метаданные: текст инъекции в лог не
// попадает (в нём могут быть чувствительные строки).
func TestDescribeAppliedAndSkippedNoContent(t *testing.T) {
	const secret = "СЕКРЕТНЫЙ-ТЕКСТ-ИНЪЕКЦИИ"
	applied := []AppliedInjection{{ID: "i1", Name: "первая", Scope: "runtime", Target: "system", Position: "append"}}
	desc := DescribeApplied(applied)
	if !strings.Contains(desc, "i1/первая") {
		t.Errorf("DescribeApplied = %q", desc)
	}
	if strings.Contains(desc, secret) {
		t.Error("в описании применённых есть текст инъекции")
	}
	skipped := []SkippedInjection{{ID: "i2", Name: "вторая", Reason: "условие when ложно"}}
	desc = DescribeSkipped(skipped)
	if !strings.Contains(desc, "вторая") || !strings.Contains(desc, "when") {
		t.Errorf("DescribeSkipped = %q", desc)
	}
	if strings.Contains(desc, secret) {
		t.Error("в описании пропущенных есть текст инъекции")
	}
	if DescribeApplied(nil) != "" || DescribeSkipped(nil) != "" {
		t.Error("пустые списки должны давать пустую строку")
	}
}

// identityInjs — конструктор списка инъекций в тестах (без обёрток).
func identityInjs(injs []board.Injection) []board.Injection { return injs }

// Порядок scope: global → assistant → session → runtime.
func TestScopeOrderIsAscending(t *testing.T) {
	injs := []board.Injection{
		{Name: "runtime", Scope: "runtime"},
		{Name: "global", Scope: "global"},
		{Name: "session", Scope: "session"},
		{Name: "assistant", Scope: "assistant"},
	}
	sortInjections(identityInjs(injs))
	got := make([]string, 0, len(injs))
	for _, inj := range injs {
		got = append(got, inj.Name)
	}
	want := []string{"global", "assistant", "session", "runtime"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок = %v, want %v", got, want)
		}
	}
}

// Внутри scope сортировка по priority (убывание), при равенстве — порядок
// объявления.
func TestSortInjectionsByPriorityDescending(t *testing.T) {
	injs := identityInjs([]board.Injection{
		{Name: "low", Scope: "runtime", Priority: 1},
		{Name: "high", Scope: "runtime", Priority: 9},
		{Name: "mid", Scope: "runtime", Priority: 5},
	})
	sortInjections(injs)
	if injs[0].Name != "high" || injs[1].Name != "mid" || injs[2].Name != "low" {
		t.Errorf("порядок = %s,%s,%s; want high,mid,low", injs[0].Name, injs[1].Name, injs[2].Name)
	}
}

// ScopeOrder используется как справочник в SortInjections и совпадает с
// реальным приоритетом сортировки.
func TestScopeOrderMatchesSorting(t *testing.T) {
	order := ScopeOrder()
	injs := identityInjs([]board.Injection{
		{Name: "d", Scope: order[3]},
		{Name: "c", Scope: order[2]},
		{Name: "b", Scope: order[1]},
		{Name: "a", Scope: order[0]},
	})
	sortInjections(injs)
	for i, want := range []string{"a", "b", "c", "d"} {
		if injs[i].Name != want {
			t.Fatalf("позиция %d = %q, want %q", i, injs[i].Name, want)
		}
	}
}

func TestTruncateContent(t *testing.T) {
	if got := TruncateContent("коротко", 100); got != "коротко" {
		t.Errorf("got %q", got)
	}
	got := TruncateContent(strings.Repeat("x", 150), 100)
	if len(got) >= 150 {
		t.Errorf("не обрезано: %d байт", len(got))
	}
	if !strings.Contains(got, "[truncated]") {
		t.Error("нет маркера обрезки")
	}
}

func TestDeduplicateByIDLastWins(t *testing.T) {
	injs := identityInjs([]board.Injection{
		{ID: "a", Name: "старая", Content: "OLD"},
		{ID: "b", Name: "другая", Content: "KEEP"},
		{ID: "a", Name: "новая", Content: "NEW"},
	})
	out := DeduplicateByID(injs)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Name != "новая" || out[0].Content != "NEW" {
		t.Errorf("первая = %+v, want новая версия a", out[0])
	}
	if out[1].Name != "другая" {
		t.Errorf("вторая = %+v", out[1])
	}
}
