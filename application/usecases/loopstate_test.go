package usecases

import (
	"strings"
	"testing"
)

func TestVerifyCommandOfOnlyRun(t *testing.T) {
	if got := VerifyCommandOf("Run", map[string]any{"command": "  make test  "}); got != "make test" {
		t.Fatalf("Run: got %q, want %q", got, "make test")
	}
	if got := VerifyCommandOf("WriteFiles", map[string]any{"command": "make test"}); got != "" {
		t.Fatalf("WriteFiles: got %q, want empty", got)
	}
	if got := VerifyCommandOf("Run", nil); got != "" {
		t.Fatalf("nil args: got %q, want empty", got)
	}
}

func TestVerifyIdentityCanonicalizes(t *testing.T) {
	a := VerifyIdentity(`cd frontend && npm test -- --run src/pages/Login.test.tsx 2>&1 | grep -B5 -A30 "FAIL"`)
	b := VerifyIdentity(`cd web && npm test -- --run src/pages/Login.test.tsx 2>&1 | grep -i "console"`)
	if !strings.Contains(a, "npm test") || !strings.Contains(a, "src/pages/Login.test.tsx") {
		t.Fatalf("identity потерял программу/файл: %q", a)
	}
	if strings.Contains(a, "|") || strings.Contains(a, "grep") {
		t.Fatalf("identity не убрал конвейер: %q", a)
	}
	if strings.Contains(a, "frontend") {
		t.Fatalf("identity сохранил каталог cd: %q", a)
	}
	if a != b {
		t.Fatalf("каталог/grep-флаги не нормализованы:\n a=%q\n b=%q", a, b)
	}
}

func TestLoopIsVerificationCommand(t *testing.T) {
	for _, cmd := range []string{"make test", "go test ./...", "npm run build", "eslint .", "tsc --noEmit"} {
		if !LoopIsVerificationCommand(cmd) {
			t.Errorf("%q должно считаться проверкой", cmd)
		}
	}
	for _, cmd := range []string{"git status", "ls -la", "cat README.md"} {
		if LoopIsVerificationCommand(cmd) {
			t.Errorf("%q не проверка", cmd)
		}
	}
}

func TestLoopStateFromCall(t *testing.T) {
	failed := []byte(`{"status":"error","stderr":"FAIL src/a.test.tsx:10:1\nExpected 1 to be 2"}`)
	state, sample, label := LoopStateFromCall("Run", map[string]any{"command": "make frontend-test"}, failed)
	if state == "" || sample == "" || label != "make frontend-test" {
		t.Fatalf("падение проверки: state=%q sample=%q label=%q", state, sample, label)
	}

	if s, _, _ := LoopStateFromCall("Run", map[string]any{"command": "make frontend-test"}, []byte(`{"status":"success"}`)); s != "" {
		t.Fatalf("успешная проверка не должна давать состояние: %q", s)
	}
	if s, _, _ := LoopStateFromCall("Run", map[string]any{"command": "git status"}, failed); s != "" {
		t.Fatalf("git status не проверка: %q", s)
	}
	if s, _, _ := LoopStateFromCall("WriteFiles", nil, failed); s != "" {
		t.Fatalf("WriteFiles не проверка: %q", s)
	}
}

func TestNormalizeVerifyOutputCollapsesNoise(t *testing.T) {
	// Одинаковая ошибка, разный шум: разные пути, тайминги, счётчики.
	first := "FAIL /build/abc123/src/a.test.tsx:10:1\n  at 12:34:56 in 120ms [3/8]"
	second := "FAIL /build/zzz999/src/a.test.tsx:10:1\n  at 23:45:01 in 340ms [5/9]"
	if NormalizeVerifyOutput(first) != NormalizeVerifyOutput(second) {
		t.Fatalf("шум не нормализован:\n%s\n---\n%s", NormalizeVerifyOutput(first), NormalizeVerifyOutput(second))
	}
}

func TestLoopVerifyInjectionMentionsIdentity(t *testing.T) {
	msg := LoopVerifyInjection("make test", "cd … && make test", "FAIL", 3)
	if !strings.Contains(msg, "cd … && make test") {
		t.Fatalf("вмешательство не содержит identity: %q", msg)
	}
	if !strings.Contains(msg, "3") {
		t.Fatalf("вмешательство не содержит число раундов: %q", msg)
	}
}
