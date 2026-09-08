package tools

import "testing"

func TestParseBugReportsNamedArgs(t *testing.T) {
	content := `Отчёт о тестировании.
BugReport(file="server/internal/order/service.go", line=42, severity="blocker", text="не обработан пустой заказ: падает с nil pointer")
BugReport(file=frontend/src/api.ts, text="не совпадает URL с контрактом")`
	bugs := ParseBugReports(content)
	if len(bugs) != 2 {
		t.Fatalf("bugs: got %d, want 2", len(bugs))
	}
	if bugs[0].FilePath != "server/internal/order/service.go" {
		t.Fatalf("bugs[0].FilePath: got %q", bugs[0].FilePath)
	}
	if bugs[0].Line != 42 {
		t.Fatalf("bugs[0].Line: got %d, want 42", bugs[0].Line)
	}
	if bugs[0].Severity != "blocker" {
		t.Fatalf("bugs[0].Severity: got %q", bugs[0].Severity)
	}
	if bugs[1].Severity != "" || bugs[1].Line != 0 {
		t.Fatalf("bugs[1] должен быть без line/severity, got %+v", bugs[1])
	}
}

func TestParseBugReportsPositional(t *testing.T) {
	content := `BugReport('server/main.go', 5, 'баг: /health возвращает 500')`
	bugs := ParseBugReports(content)
	if len(bugs) != 1 {
		t.Fatalf("bugs: got %d, want 1", len(bugs))
	}
	if bugs[0].FilePath != "server/main.go" || bugs[0].Line != 5 {
		t.Fatalf("bug: got %+v", bugs[0])
	}
}

func TestParseBugReportsNone(t *testing.T) {
	if bugs := ParseBugReports("тесты пройдены, дефектов нет"); bugs != nil {
		t.Fatalf("ожидали nil, got %v", bugs)
	}
	if bugs := ParseBugReports(""); bugs != nil {
		t.Fatalf("ожидали nil для пустого текста, got %v", bugs)
	}
}

func TestParseBugReportsTrimsDotPrefix(t *testing.T) {
	bugs := ParseBugReports(`BugReport(file="./frontend/App.tsx", text="баг")`)
	if len(bugs) != 1 || bugs[0].FilePath != "frontend/App.tsx" {
		t.Fatalf("bugs: got %+v", bugs)
	}
}
