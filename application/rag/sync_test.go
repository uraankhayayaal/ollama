// Тесты синхронизации рабочей копии с main (application/rag/sync.go): пропуски
// (не-git, грязное дерево, занятый checkout), откат конфликтного merge и признак
// фактического обновления (сдвиг HEAD). Git подменяется hermetic-исполнителем.
package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai/infrastructure/git"
)

type syncExec struct {
	status   string // вывод `git status --porcelain`
	headSeq  []string
	headIdx  int
	mergeErr error
	aborts   int
	calls    []string
}

func (e *syncExec) Exec(_ context.Context, _ string, argv ...string) (string, error) {
	cmd := strings.Join(argv, " ")
	e.calls = append(e.calls, cmd)
	switch {
	case cmd == "git status --porcelain":
		return e.status, nil
	case cmd == "git merge --abort":
		e.aborts++
		return "", nil
	case strings.HasPrefix(cmd, "git merge --no-ff"):
		if e.mergeErr != nil {
			return "", e.mergeErr
		}
		return "", nil
	case strings.HasPrefix(cmd, "git rev-parse HEAD"):
		if len(e.headSeq) == 0 {
			return "h0", nil
		}
		i := e.headIdx
		if i >= len(e.headSeq) {
			i = len(e.headSeq) - 1
		}
		e.headIdx++
		return e.headSeq[i], nil
	}
	return "", nil
}

func (e *syncExec) saw(sub string) bool {
	for _, c := range e.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

// syncSinkRec — SyncSink, запоминающий уровни сообщений.
type syncSinkRec struct{ details, warns, infos []string }

func (s *syncSinkRec) Detail(f string, a ...any) { s.details = append(s.details, f) }
func (s *syncSinkRec) Warn(f string, a ...any)   { s.warns = append(s.warns, f) }
func (s *syncSinkRec) Info(f string, a ...any)   { s.infos = append(s.infos, f) }

func newSyncRepo(ex *syncExec) *git.Repo {
	return git.RepoFromState(ex, "/tmp/repo", "origin", "ai/proj", "main")
}

func TestSyncSkipsInvalidInput(t *testing.T) {
	ex := &syncExec{}
	sink := &syncSinkRec{}
	w := WorkingCopySyncer{Exec: ex, Sink: sink}

	if w.Sync(context.Background(), SyncProject{Base: "", Branch: "ai/proj"}, newSyncRepo(ex)) {
		t.Fatal("пустая база — false")
	}
	if w.Sync(context.Background(), SyncProject{Base: "main", Branch: ""}, newSyncRepo(ex)) {
		t.Fatal("пустая ветка — false")
	}
	if w.Sync(context.Background(), SyncProject{Base: "main", Branch: "main"}, newSyncRepo(ex)) {
		t.Fatal("ветка == база — false")
	}
	if w.Sync(context.Background(), SyncProject{Base: "main", Branch: "ai/proj"}, nil) {
		t.Fatal("nil repo — false")
	}
	if len(ex.calls) != 0 {
		t.Fatalf("при пропусках git не вызывается: %v", ex.calls)
	}
}

func TestSyncSkipsOccupiedCheckout(t *testing.T) {
	ex := &syncExec{}
	w := WorkingCopySyncer{Exec: ex, Sink: &syncSinkRec{}}
	repo := git.RepoFromState(ex, "/tmp/repo", "origin", "ai/other", "main")
	if w.Sync(context.Background(), SyncProject{Base: "main", Branch: "ai/proj"}, repo) {
		t.Fatal("занятый checkout — false")
	}
	if ex.saw("merge --no-ff") {
		t.Fatalf("merge не выполняется: %v", ex.calls)
	}
}

func TestSyncSkipsDirtyTree(t *testing.T) {
	ex := &syncExec{status: " M server/api.go\n"}
	w := WorkingCopySyncer{Exec: ex, Sink: &syncSinkRec{}}
	if w.Sync(context.Background(), SyncProject{Base: "main", Branch: "ai/proj"}, newSyncRepo(ex)) {
		t.Fatal("грязное дерево — false")
	}
	if ex.saw("merge --no-ff") {
		t.Fatalf("merge не выполняется при грязном дереве: %v", ex.calls)
	}
}

func TestSyncAbortsOnMergeConflict(t *testing.T) {
	ex := &syncExec{mergeErr: errors.New("conflict")}
	sink := &syncSinkRec{}
	w := WorkingCopySyncer{Exec: ex, Sink: sink}
	if w.Sync(context.Background(), SyncProject{Base: "main", Branch: "ai/proj"}, newSyncRepo(ex)) {
		t.Fatal("конфликт — false")
	}
	if ex.aborts != 1 {
		t.Fatalf("ожидался откат merge, aborts=%d", ex.aborts)
	}
	if len(sink.warns) != 1 {
		t.Fatalf("ожидалось предупреждение, got %v", sink.warns)
	}
}

func TestSyncHeadShiftReturnsTrue(t *testing.T) {
	ex := &syncExec{headSeq: []string{"h1", "h2"}}
	sink := &syncSinkRec{}
	w := WorkingCopySyncer{Exec: ex, Sink: sink}
	if !w.Sync(context.Background(), SyncProject{Base: "main", Branch: "ai/proj"}, newSyncRepo(ex)) {
		t.Fatal("сдвиг HEAD — true")
	}
	if !ex.saw("git merge --no-ff -m ai/proj: синхрон рабочей копии с main main") {
		t.Fatalf("сообщение merge неверно: %v", ex.calls)
	}
	if len(sink.infos) != 1 {
		t.Fatalf("ожидался info об синхронизации: %v", sink.infos)
	}
}

func TestSyncHeadUnchangedReturnsFalse(t *testing.T) {
	ex := &syncExec{headSeq: []string{"h1", "h1"}}
	sink := &syncSinkRec{}
	w := WorkingCopySyncer{Exec: ex, Sink: sink}
	if w.Sync(context.Background(), SyncProject{Base: "main", Branch: "ai/proj"}, newSyncRepo(ex)) {
		t.Fatal("HEAD не сдвинулся — false")
	}
	if len(sink.infos) != 0 {
		t.Fatalf("info не должно быть: %v", sink.infos)
	}
}
