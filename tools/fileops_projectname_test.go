package tools

import "testing"

// ProjectName — имя проекта для RAG: явное Project приоритетно (worktree задачи
// называется .wt-task-<проект>-<id>, и без явного имени поиск шёл бы по
// несуществующему «проекту»), иначе basename рабочего каталога; пусто — "".
// Сама логика фолбэка живёт в FileOps, поэтому покрыта здесь (тесты CodeSearch в
// tools/rag используют fake-контекст).
func TestFileOpsProjectName(t *testing.T) {
	if got := (&FileOps{OutputDir: "/tmp/temp/billingService"}).ProjectName(); got != "billingService" {
		t.Fatalf("basename рабочего каталога: got %q, want billingService", got)
	}
	if got := (&FileOps{OutputDir: "/tmp/temp/.wt-task-billingService-T-01", Project: "billingService"}).ProjectName(); got != "billingService" {
		t.Fatalf("явное имя проекта: got %q, want billingService", got)
	}
	if got := (&FileOps{OutputDir: "/tmp/temp/.wt-task-billingService-T-01"}).ProjectName(); got != ".wt-task-billingService-T-01" {
		t.Fatalf("worktree без явного имени: got %q, want .wt-task-billingService-T-01", got)
	}
	if got := (*FileOps)(nil).ProjectName(); got != "" {
		t.Fatalf("nil FileOps: got %q, want ''", got)
	}
	if got := (&FileOps{OutputDir: ""}).ProjectName(); got != "" {
		t.Fatalf("пустой рабочий каталог: got %q, want ''", got)
	}
}

// WorkingDir — ленивый nil-безопасный аксессор рабочего каталога: подпакеты
// (tools/rag) читают OutputDir через интерфейс, а не запоминают значение при
// сборке набора инструментов (SetOutputDir меняет его на лету).
func TestFileOpsWorkingDir(t *testing.T) {
	ops := &FileOps{OutputDir: "/tmp/temp/demo"}
	if got := ops.WorkingDir(); got != "/tmp/temp/demo" {
		t.Fatalf("WorkingDir: got %q", got)
	}
	ops.SetOutputDir("/tmp/temp/.wt-task-demo-T-01")
	if got := ops.WorkingDir(); got != "/tmp/temp/.wt-task-demo-T-01" {
		t.Fatalf("WorkingDir после SetOutputDir: got %q", got)
	}
	if got := (*FileOps)(nil).WorkingDir(); got != "" {
		t.Fatalf("nil FileOps: got %q, want ''", got)
	}
}
