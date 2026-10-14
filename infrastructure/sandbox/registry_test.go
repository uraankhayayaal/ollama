package sandbox

import (
	"context"
	"testing"
)

// fakeWS — поддельный Workspace: проверяет маршрутизацию реестра, не трогая
// Docker.
type fakeWS struct {
	runs   int
	closed bool
}

func (f *fakeWS) Run(context.Context, string, string) (Result, error) {
	f.runs++
	return Result{ExitCode: 0}, nil
}
func (f *fakeWS) Close(context.Context) error { f.closed = true; return nil }

func TestCoverLenRootAndWorktree(t *testing.T) {
	e := entry{Proj: "mytrip", Dir: "/tmp/temp/mytrip"}

	if got := e.coverLen("/tmp/temp/mytrip"); got < 0 {
		t.Error("корень проекта должен покрываться")
	}
	if got := e.coverLen("/tmp/temp/mytrip/frontend"); got < 0 {
		t.Error("подкаталог проекта должен покрываться")
	}
	// Worktree задачи — dot-сосед с именем проекта.
	if got := e.coverLen("/tmp/temp/.wt-task-mytrip-FEL-01"); got < 0 {
		t.Error("worktree проекта (.wt-task-mytrip-…) должен покрываться")
	}
	// Чужой worktree в общем temp/ — НЕ должен покрываться: иначе контейнер
	// одной сессии выполнял бы команды другой.
	if got := e.coverLen("/tmp/temp/.wt-task-other-FEL-01"); got >= 0 {
		t.Error("worktree чужого проекта не должен покрываться")
	}
	// Недот-сосед (не скрытый каталог) — не worktree.
	if got := e.coverLen("/tmp/temp/mytrip2"); got >= 0 {
		t.Error("каталог без точки в начале — не worktree")
	}
	// Соседняя ветвь дерева.
	if got := e.coverLen("/tmp/other/mytrip"); got >= 0 {
		t.Error("чужое дерево не должно покрываться")
	}
}

func TestLookupPrefersExactProjectAndBestCoverage(t *testing.T) {
	a := &fakeWS{}
	b := &fakeWS{}
	Activate("proj-a", a, "/tmp/temp/proj-a", []string{"/tmp/temp"})
	defer Deactivate("proj-a")
	Activate("proj-b", b, "/tmp/temp/proj-b", []string{"/tmp/temp"})
	defer Deactivate("proj-b")

	// Точное имя проекта важнее.
	if got := Lookup("proj-a", "/tmp/temp/proj-a"); got != Workspace(a) {
		t.Error("точное имя проекта должно матчиться в первую очередь")
	}
	// Пустое имя — наилучшее покрытие по пути.
	if got := Lookup("", "/tmp/temp/proj-b/frontend"); got != Workspace(b) {
		t.Error("без имени проекта ожидали наилучшее покрытие по пути")
	}
	// Неизвестный каталог — nil (вызывающий решает: хост или отказ).
	if got := Lookup("proj-a", "/var/elsewhere"); got != nil {
		t.Errorf("чужой каталог не должен матчиться, получено %v", got)
	}
	// Имя проекта известно, но каталог чужой — чужая сессия не подхватывает.
	if got := Lookup("proj-a", "/tmp/temp/proj-b"); got != nil {
		t.Errorf("proj-a не должен матчить чужой каталог, получено %v", got)
	}
}

func TestStopSessionClosesAndIsIdempotent(t *testing.T) {
	f := &fakeWS{}
	Activate("stopme", f, "/tmp/temp/stopme", []string{"/tmp/temp/stopme"})
	if err := StopSession(context.Background(), "stopme"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if !f.closed {
		t.Error("Close обязан вызваться при остановке сессии")
	}
	if Lookup("stopme", "/tmp/temp/stopme") != nil {
		t.Error("после StopSession запись должна уйти из реестра")
	}
	// Повторная остановка — no-op, а не ошибка.
	if err := StopSession(context.Background(), "stopme"); err != nil {
		t.Errorf("повторный StopSession: %v", err)
	}
}

func TestActivateReplacesOldWorkspace(t *testing.T) {
	old := &fakeWS{}
	newWS := &fakeWS{}
	Activate("replace", old, "/tmp/temp/replace", nil)
	Activate("replace", newWS, "/tmp/temp/replace", nil)
	if got := Lookup("replace", "/tmp/temp/replace"); got != Workspace(newWS) {
		t.Error("повторная активация должна подменять запись")
	}
	if Deactivate("replace") != Workspace(newWS) {
		t.Error("Deactivate должен возвращать текущий Workspace")
	}
}

func TestActiveProjects(t *testing.T) {
	Activate("ap-1", &fakeWS{}, "/tmp/temp/ap-1", nil)
	defer Deactivate("ap-1")
	for _, p := range ActiveProjects() {
		if p == "ap-1" {
			return
		}
	}
	t.Error("ActiveProjects не показал активную сессию")
}
