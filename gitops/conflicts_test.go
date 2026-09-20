package gitops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanConflictBlocks(t *testing.T) {
	content := "" +
		"a\n" +
		"<<<<<<< HEAD\n" +
		"our-line\n" +
		"=======\n" +
		"their-line\n" +
		">>>>>>> ai/epic/e1\n" +
		"b\n"
	blocks, has := ScanConflictBlocks(content)
	if !has || len(blocks) != 1 {
		t.Fatalf("ScanConflictBlocks = %d блоков, has=%v", len(blocks), has)
	}
	if blocks[0].Our != "our-line" || blocks[0].Their != "their-line" {
		t.Fatalf("блок = %+v", blocks[0])
	}

	if blocks, has := ScanConflictBlocks("без маркеров\n"); has || len(blocks) != 0 {
		t.Fatalf("чистый файл: blocks=%v has=%v", blocks, has)
	}
}

func TestScanConflictBlocksBrokenMarkers(t *testing.T) {
	cases := []string{
		"<<<<<<< HEAD\nours\n",                                           // нет разделителя
		"<<<<<<< HEAD\nours\n=======\ntheirs\n",                          // нет закрывающего
		"text\n<<<<<<< HEAD\n=======\ntheirs\n>>>>>>> x\n<<<<<<< HEAD\n", // хвост
	}
	for _, c := range cases {
		if blocks, has := ScanConflictBlocks(c); has {
			t.Fatalf("битые маркеры должны давать has=false: blocks=%v", blocks)
		}
	}
}

func TestResolveConflictMarkers(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   string
		resolv bool
	}{
		{
			name: "идентичные стороны → наша",
			in:   "x\n<<<<<<< HEAD\nsame\n=======\nsame\n>>>>>>> b\ny\n",
			want: "x\nsame\ny\n",
			resolv: true,
		},
		{
			name: "обе стороны пустые → ничего",
			in:   "<<<<<<< HEAD\n=======\n>>>>>>> b\nz\n",
			want: "z\n",
			resolv: true,
		},
		{
			name: "только наша (пустая чужая)",
			in:   "a\n<<<<<<< HEAD\nour\n=======\n>>>>>>> b\n",
			want: "a\nour\n",
			resolv: true,
		},
		{
			name: "только чужая (пустая наша) → чужая",
			in:   "a\n<<<<<<< HEAD\n=======\ntheir\n>>>>>>> b\n",
			want: "a\ntheir\n",
			resolv: true,
		},
		{
			name: "отличаются только пробелами → наша",
			in:   "p\n<<<<<<< HEAD\n\tif ok {\n\t\trun()\n\t}\n=======\n    if ok {\n        run()\n    }\n>>>>>>> b\n",
			want: "p\n\tif ok {\n\t\trun()\n\t}\n",
			resolv: true,
		},
		{
			name:   "разные правки → не решаем",
			in:     "p\n<<<<<<< HEAD\nkeep-this\n=======\nkeep-that\n>>>>>>> b\n",
			resolv: false,
		},
		{
			name:   "два блока: тривиальный и сложный → не решаем целиком",
			in:     "1\n<<<<<<< HEAD\nsame\n=======\nsame\n>>>>>>> b\n<<<<<<< HEAD\nA\n=======\nB\n>>>>>>> b\n",
			resolv: false,
		},
		{
			name:   "без маркеров — без изменений",
			in:     "plain\nfile\n",
			want:   "plain\nfile\n",
			resolv: true,
		},
		{
			name:   "пустой файл",
			in:     "",
			want:   "",
			resolv: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResolveConflictMarkers(tc.in)
			if ok != tc.resolv {
				t.Fatalf("ResolveConflictMarkers ok=%v, want %v", ok, tc.resolv)
			}
			if !tc.resolv {
				if got != tc.in {
					t.Fatalf("сложный файл должен остаться нетронутым: %q", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("на выходе:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestTrivialResolveDir(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"same.txt":     "x\n<<<<<<< HEAD\ns\n=======\ns\n>>>>>>> b\ny\n", // тривиальный
		"ours.txt":     "<<<<<<< HEAD\na\n=======\n>>>>>>> b\n",          // одна сторона
		"hard.txt":     "<<<<<<< HEAD\nA\n=======\nB\n>>>>>>> b\n",       // сложный
		"broken.txt":   "<<<<<<< HEAD\nA\n=======\nB",                    // битые маркеры
		"clean.txt":    "чисто\n",                                        // без маркеров
		"missing.txt":  "",                                               // нет на диске → hard
		"trailing.txt": "t\n<<<<<<< HEAD\ns\n=======\ns\n>>>>>>> b\n",    // завершающий перенос
	}
	for name, content := range files {
		if name == "missing.txt" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	resolved, hard, err := TrivialResolve(dir, []string{
		"same.txt", "ours.txt", "hard.txt", "broken.txt", "clean.txt", "missing.txt", "trailing.txt",
	})
	if err != nil {
		t.Fatalf("TrivialResolve: %v", err)
	}

	if got := strings.Join(resolved, ","); got != "same.txt,ours.txt,clean.txt,trailing.txt" {
		t.Fatalf("resolved = %q", got)
	}
	if got := strings.Join(hard, ","); got != "hard.txt,broken.txt,missing.txt" {
		t.Fatalf("hard = %q", got)
	}

	assertFile := func(name, want string) {
		data, _ := os.ReadFile(filepath.Join(dir, name))
		if string(data) != want {
			t.Fatalf("%s:\n%q\nwant:\n%q", name, data, want)
		}
	}
	assertFile("same.txt", "x\ns\ny\n")
	assertFile("ours.txt", "a\n")
	assertFile("trailing.txt", "t\ns\n")
	assertFile("hard.txt", files["hard.txt"]) // маркеры на месте
	assertFile("broken.txt", files["broken.txt"])
	assertFile("clean.txt", files["clean.txt"])
}

func TestSafePathRel(t *testing.T) {
	if safePathRel("a/b.txt") == false {
		t.Fatal("нормальный путь должен проходить")
	}
	for _, p := range []string{"", ".", "..", "../x", "a/../../x", "/abs", "./../x"} {
		if safePathRel(p) {
			t.Fatalf("небезопасный путь %q пропущен", p)
		}
	}
}

func TestUnmergedFilesDryRun(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{resp: map[string]string{
		"/wt | git ls-files -u": "" +
			"100644 c0d0f 1	main.go\n" +
			"100644 ffdfb 2	main.go\n" +
			"100644 54265 3	main.go\n" +
			"100644 00000 2	new.go\n" +
			"100644 11111 3	new.go\n" +
			"100644 aaa 2	my file.txt\n",
	}}
	repo := &Repo{Root: "/wt", ex: ex}
	files, err := repo.UnmergedFiles(ctx)
	if err != nil {
		t.Fatalf("UnmergedFiles: %v", err)
	}
	if got := strings.Join(files, ","); got != "main.go,new.go,my file.txt" {
		t.Fatalf("UnmergedFiles = %q", got)
	}
	want := "/wt | git ls-files -u"
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\nwant:\n%s", got, want)
	}
}

func TestStageDryRun(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{}
	repo := &Repo{Root: "/wt", ex: ex}
	if err := repo.Stage(ctx, []string{"a.go", "b.go"}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	want := "/wt | git add -- a.go b.go"
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\nwant:\n%s", got, want)
	}
}

func TestStageNoopOnEmpty(t *testing.T) {
	ex := &fakeExecutor{}
	if err := (&Repo{Root: "/wt", ex: ex}).Stage(context.Background(), nil); err != nil {
		t.Fatalf("Stage(nil): %v", err)
	}
	if len(ex.calls) != 0 {
		t.Fatalf("пустой список не должен вызывать git: %v", ex.calls)
	}
}

func TestConflictDiffDryRun(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{resp: map[string]string{
		"/wt | git diff -- a.go b.go": "diff --git a/a.go b/a.go\n",
	}}
	repo := &Repo{Root: "/wt", ex: ex}
	out, err := repo.ConflictDiff(ctx, []string{"a.go", "b.go"})
	if err != nil || !strings.Contains(out, "diff --git") {
		t.Fatalf("ConflictDiff = %q, %v", out, err)
	}
	want := "/wt | git diff -- a.go b.go"
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\nwant:\n%s", got, want)
	}
}

func TestAddWorktreeDryRun(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{}
	repo := &Repo{Root: "/clone", ex: ex}
	wt, err := repo.AddWorktree(ctx, "/wt/.conflict", "ai/epic/e1")
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if wt.Root != "/wt/.conflict" || wt.Branch != "ai/epic/e1" || wt.Base != "ai/epic/e1" {
		t.Fatalf("worktree Repo = %+v", wt)
	}
	want := "/clone | git worktree add /wt/.conflict ai/epic/e1"
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\nwant:\n%s", got, want)
	}
}

func TestRemoveWorktreeDryRun(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{}
	repo := &Repo{Root: "/clone", ex: ex}
	if err := repo.RemoveWorktree(ctx, "/wt/.conflict"); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	want := "/clone | git worktree remove --force /wt/.conflict"
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\nwant:\n%s", got, want)
	}
}

func TestHasConflictMarkers(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "hard.txt"),
		[]byte("a\n<<<<<<< HEAD\nx\n=======\ny\n>>>>>>> b\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "clean.txt"), []byte("ok\n"), 0o644)

	found, err := HasConflictMarkers(dir, []string{"hard.txt", "clean.txt", "gone.txt"})
	if err != nil {
		t.Fatalf("HasConflictMarkers: %v", err)
	}
	if got := strings.Join(found, ","); got != "hard.txt" {
		t.Fatalf("маркеры = %q", got)
	}
}

func TestMarkerLinesEdge(t *testing.T) {
	// Строка «=======» без лидирующего контекста — разделитель, не часть кода.
	content := "=== summary ===\n<<<<<<< HEAD\na\n=======\nb\n>>>>>>> r\n"
	if _, has := ScanConflictBlocks(content); !has {
		t.Fatal("маркеры внутри текста должны детектироваться")
	}
}
