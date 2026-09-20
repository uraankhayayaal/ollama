package gitops

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestMergeFeatureHappyDryRun(t *testing.T) {
	ctx := context.Background()
	const wtPath = "/.wt-ai_task_t1"
	ex := &fakeExecutor{resp: map[string]string{
		"/clone | git merge-base ai/epic/e1 ai/task/t1":      "beef\n",
		"/clone | git rev-parse ai/task/t1":                  "cafe\n",
		"/clone | git merge-tree beef ai/epic/e1 ai/task/t1": "",
	}}
	repo := &Repo{Root: "/clone", ex: ex}
	res, err := repo.MergeFeature(ctx, "ai/epic/e1", "ai/task/t1", MergeFeatureOptions{
		Message: "задача t1 влита в релиз",
		PushURL: "git@gitlab.com:g/myrepo.git",
	})
	if err != nil {
		t.Fatalf("MergeFeature: %v", err)
	}
	if res.AlreadyMerged || res.Message != "задача t1 влита в релиз" {
		t.Fatalf("MergeResult = %+v", res)
	}
	want := "/clone | git merge-base ai/epic/e1 ai/task/t1\n" +
		"/clone | git rev-parse ai/task/t1\n" +
		"/clone | git merge-base ai/epic/e1 ai/task/t1\n" +
		"/clone | git merge-tree beef ai/epic/e1 ai/task/t1\n" +
		"/clone | git worktree add " + wtPath + " ai/epic/e1\n" +
		wtPath + " | git merge --no-ff -m задача t1 влита в релиз ai/task/t1\n" +
		wtPath + " | git push git@gitlab.com:g/myrepo.git ai/epic/e1\n" +
		"/clone | git worktree remove --force " + wtPath
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestMergeFeatureConflicts(t *testing.T) {
	ctx := context.Background()
	const conflicts = `changed in both
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 f.txt
  our    100644 ffdfb012e4aec2a6d3c21bb473f19822c5737852 f.txt
  their  100644 542655f4d59aeaeb83b65316f9bc779f6ffb11f9 f.txt
@@ -1,2 +1,6 @@
+<<<<<<< .our
+=======
+>>>>>>> .their
`
	ex := &fakeExecutor{resp: map[string]string{
		"/clone | git merge-base ai/epic/e1 ai/task/t1":      "beef\n",
		"/clone | git rev-parse ai/task/t1":                  "cafe\n",
		"/clone | git merge-tree beef ai/epic/e1 ai/task/t1": conflicts,
	}}
	repo := &Repo{Root: "/clone", ex: ex}
	_, err := repo.MergeFeature(ctx, "ai/epic/e1", "ai/task/t1", MergeFeatureOptions{Message: "m"})
	if err == nil {
		t.Fatal("ожидали ошибку конфликта")
	}
	var ce *MergeConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("ошибка не MergeConflictError: %v", err)
	}
	if ce.Feature != "ai/task/t1" || len(ce.Files) != 1 || ce.Files[0] != "f.txt" {
		t.Fatalf("MergeConflictError = %+v", ce)
	}
	// Конфликт обнаружен до создания worktree — рабочие команды не вызывались.
	if n := len(ex.calls); n != 4 {
		t.Fatalf("вызовов до конфликта: %d (want 4), calls: %v", n, ex.calls)
	}
}

func TestMergeFeatureAlreadyMerged(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{resp: map[string]string{
		"/clone | git merge-base ai/epic/e1 ai/task/t1": "cafe\n",
		"/clone | git rev-parse ai/task/t1":             "cafe\n",
	}}
	repo := &Repo{Root: "/clone", ex: ex}
	res, err := repo.MergeFeature(ctx, "ai/epic/e1", "ai/task/t1", MergeFeatureOptions{Message: "m"})
	if err != nil {
		t.Fatalf("MergeFeature: %v", err)
	}
	if !res.AlreadyMerged {
		t.Fatalf("AlreadyMerged = false, want true")
	}
	// Никаких мутаций: только проверка идемпотентности.
	if n := len(ex.calls); n != 2 {
		t.Fatalf("вызовов: %d, want 2; calls: %v", n, ex.calls)
	}
}

func TestMergeFeatureNoPushWhenURLAbsent(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{resp: map[string]string{
		"/clone | git merge-base ai/epic/e1 ai/task/t1":      "beef\n",
		"/clone | git rev-parse ai/task/t1":                  "cafe\n",
		"/clone | git merge-tree beef ai/epic/e1 ai/task/t1": "",
	}}
	repo := &Repo{Root: "/clone", ex: ex}
	if _, err := repo.MergeFeature(ctx, "ai/epic/e1", "ai/task/t1", MergeFeatureOptions{Message: "m"}); err != nil {
		t.Fatalf("MergeFeature: %v", err)
	}
	for _, c := range ex.calls {
		if strings.Contains(c, "git push ") {
			t.Fatalf("без PushURL не должно быть push, вызовы: %v", ex.calls)
		}
	}
}

func TestMergeFeatureRequiresArgs(t *testing.T) {
	ex := &fakeExecutor{}
	call := func(release, feature, msg string) error {
		_, err := (&Repo{Root: "/x", ex: ex}).MergeFeature(context.Background(), release, feature, MergeFeatureOptions{Message: msg})
		return err
	}
	if err := call("", "ai/task/t", "m"); err == nil {
		t.Fatal("пустая релизная ветка — ожидали ошибку")
	}
	if err := call("ai/epic/e", "", "m"); err == nil {
		t.Fatal("пустая фича-ветка — ожидали ошибку")
	}
	if err := call("ai/epic/e", "ai/task/t", "  "); err == nil {
		t.Fatal("пустое сообщение — ожидали ошибку")
	}
}

func TestMergedIntoDryRun(t *testing.T) {
	ctx := context.Background()
	notMerged := &fakeExecutor{resp: map[string]string{
		"/clone | git merge-base ai/epic/e1 ai/task/t1": "beef\n",
		"/clone | git rev-parse ai/task/t1":             "cafe\n",
	}}
	ok, err := (&Repo{Root: "/clone", ex: notMerged}).MergedInto(ctx, "ai/epic/e1", "ai/task/t1")
	if err != nil || ok {
		t.Fatalf("MergedInto (не слито) = %v, %v; want false", ok, err)
	}

	merged := &fakeExecutor{resp: map[string]string{
		"/clone | git merge-base ai/epic/e1 ai/task/t1": "cafe\n",
		"/clone | git rev-parse ai/task/t1":             "cafe\n",
	}}
	ok, err = (&Repo{Root: "/clone", ex: merged}).MergedInto(ctx, "ai/epic/e1", "ai/task/t1")
	if err != nil || !ok {
		t.Fatalf("MergedInto (слито) = %v, %v; want true", ok, err)
	}
}
