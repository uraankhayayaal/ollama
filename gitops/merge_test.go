package gitops

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// errFakeMissing — ошибка исполнителя для отсутствующей ветки (rev-parse).
var errFakeMissing = errors.New("ветка не найдена")

func TestMergeBranchDryRun(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{}
	repo := &Repo{Root: "/clone", Branch: "ai/epic/e1", ex: ex}
	res, err := repo.MergeBranch(ctx, "ai/task/t1", "вливание задачи t1")
	if err != nil {
		t.Fatalf("MergeBranch: %v", err)
	}
	if res.Message != "вливание задачи t1" || res.FastForward {
		t.Fatalf("MergeResult = %+v", res)
	}
	want := "/clone | git merge --no-ff -m вливание задачи t1 ai/task/t1"
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestMergeBranchRequiresMessageAndBranch(t *testing.T) {
	repo := &Repo{Root: "/x", ex: &fakeExecutor{}}
	if _, err := repo.MergeBranch(context.Background(), "", "msg"); err == nil {
		t.Fatal("пустая ветка — ожидали ошибку")
	}
	if _, err := repo.MergeBranch(context.Background(), "ai/task/t", "  "); err == nil {
		t.Fatal("пустое сообщение — ожидали ошибку")
	}
}

func TestMergeBaseDryRun(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{resp: map[string]string{
		"/clone | git merge-base HEAD ai/task/t1": "abc123\n",
	}}
	repo := &Repo{Root: "/clone", ex: ex}
	base, err := repo.MergeBase(ctx, "HEAD", "ai/task/t1")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if base != "abc123" {
		t.Fatalf("MergeBase = %q", base)
	}
}

func TestConflictingFilesDryRun(t *testing.T) {
	ctx := context.Background()
	const conflicts = `changed in both
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 web/f.txt
  our    100644 ffdfb012e4aec2a6d3c21bb473f19822c5737852 web/f.txt
  their  100644 542655f4d59aeaeb83b65316f9bc779f6ffb11f9 web/f.txt
@@ -1,2 +1,6 @@
+<<<<<<< .our
 line1-b1
+=======
+line1-b2
+>>>>>>> .their
 line2
`
	ex := &fakeExecutor{resp: map[string]string{
		"/clone | git merge-base HEAD ai/task/t1":      "beef\n",
		"/clone | git merge-tree beef HEAD ai/task/t1": conflicts,
	}}
	repo := &Repo{Root: "/clone", ex: ex}
	files, err := repo.ConflictingFiles(ctx, "ai/task/t1")
	if err != nil {
		t.Fatalf("ConflictingFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "web/f.txt" {
		t.Fatalf("конфликтующие пути = %v, want [web/f.txt]", files)
	}
	want := "/clone | git merge-base HEAD ai/task/t1\n" +
		"/clone | git merge-tree beef HEAD ai/task/t1"
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestMergeTreeParsesConflicts(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want []string
	}{
		{
			name: "чистое слияние не даёт конфликтов",
			out: `added in remote
  their  100644 0f2287157f7cb0dd40498c7a92f74b6975fa2d57 clean.txt
@@ -0,0 +1 @@
+extra
`,
			want: nil,
		},
		{
			name: "конфликт контента",
			out: `changed in both
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 f.txt
  our    100644 ffdfb012e4aec2a6d3c21bb473f19822c5737852 f.txt
  their  100644 542655f4d59aeaeb83b65316f9bc779f6ffb11f9 f.txt
@@ -1,2 +1,6 @@
+<<<<<<< .our
 line1-b1
+=======
+line1-b2
+>>>>>>> .their
 line2
`,
			want: []string{"f.txt"},
		},
		{
			name: "путь с пробелом и чистый файл не путаются",
			out: `added in local
  our    100644 0f2287157f7cb0dd40498c7a92f74b6975fa2d57 dir/clean.go
@@ -0,0 +1 @@
+package clean
changed in both
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 my file.txt
  our    100644 ffdfb012e4aec2a6d3c21bb473f19822c5737852 my file.txt
  their  100644 542655f4d59aeaeb83b65316f9bc779f6ffb11f9 my file.txt
@@ -1,2 +1,6 @@
+<<<<<<< .our
+b1
+=======
+b2
+>>>>>>> .their
`,
			want: []string{"my file.txt"},
		},
		{
			name: "переименование/удаление даёт конфликт",
			out: `removed in local
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 old.go
  our    100644 0000000000000000000000000000000000000000 old.go
changed in both
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 old.go
  our    100644 0000000000000000000000000000000000000000 old.go
  their  100644 542655f4d59aeaeb83b65316f9bc779f6ffb11f9 old.go
@@ -1,2 +1,6 @@
+<<<<<<< .our
+=======
+their line
+>>>>>>> .their
`,
			want: []string{"old.go"},
		},
		{
			name: "два конфликтующих файла",
			out: `changed in both
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 a.txt
  our    100644 ffdfb012e4aec2a6d3c21bb473f19822c5737852 a.txt
  their  100644 542655f4d59aeaeb83b65316f9bc779f6ffb11f9 a.txt
@@ -1 +1,5 @@
+<<<<<<< .our
+=======
+>>>>>>> .their
changed in both
  base   100644 c0d0fb45c382919737f8d0c20aaf57cf89b74af8 b.txt
  our    100644 ffdfb012e4aec2a6d3c21bb473f19822c5737852 b.txt
  their  100644 542655f4d59aeaeb83b65316f9bc779f6ffb11f9 b.txt
@@ -1 +1,5 @@
+<<<<<<< .our
+=======
+>>>>>>> .their
`,
			want: []string{"a.txt", "b.txt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeTreeConflicts(tc.out)
			if len(got) != len(tc.want) {
				t.Fatalf("mergeTreeConflicts = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("mergeTreeConflicts = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestCreateBranchDryRun(t *testing.T) {
	ctx := context.Background()
	ex := &fakeExecutor{}
	repo := &Repo{Root: "/clone", ex: ex}
	if err := repo.CreateBranch(ctx, "ai/epic/e1", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	want := "/clone | git branch ai/epic/e1 main"
	if got := strings.Join(ex.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestCreateBranchRequiresBranchBase(t *testing.T) {
	repo := &Repo{Root: "/x", ex: &fakeExecutor{}}
	if err := repo.CreateBranch(context.Background(), "", "main"); err == nil {
		t.Fatal("пустая ветка — ожидали ошибку")
	}
	if err := repo.CreateBranch(context.Background(), "ai/e", ""); err == nil {
		t.Fatal("пустая база — ожидали ошибку")
	}
}

func TestBranchExistsDryRun(t *testing.T) {
	ctx := context.Background()

	exists := &fakeExecutor{}
	ok, err := (&Repo{Root: "/clone", ex: exists}).BranchExists(ctx, "ai/epic/e1")
	if err != nil || !ok {
		t.Fatalf("BranchExists = %v, %v; want true", ok, err)
	}
	want := "/clone | git rev-parse --verify --quiet refs/heads/ai/epic/e1"
	if got := strings.Join(exists.calls, "\n"); got != want {
		t.Fatalf("вызовы:\n%s\n\nwant:\n%s", got, want)
	}

	missing := &fakeExecutor{failOn: map[string]error{
		"/clone | git rev-parse --verify --quiet refs/heads/ai/epic/e1": errFakeMissing,
	}}
	ok, err = (&Repo{Root: "/clone", ex: missing}).BranchExists(ctx, "ai/epic/e1")
	if err != nil || ok {
		t.Fatalf("BranchExists (нет ветки) = %v, %v; want false", ok, err)
	}
}

func TestSanitizeBranchName(t *testing.T) {
	cases := map[string]string{
		"epic-1":              "epic-1",
		"task_007":            "task_007",
		"Задача про рефактор": "Задача_про_рефактор",
		"a b":                 "a_b",
		"a..b":                "a_b", // git check-ref-format запрещает '..'
		".hidden":             "hidden",
		"  ":                  "id",
		"":                    "id",
	}
	for in, want := range cases {
		if got := SanitizeBranchName(in); got != want {
			t.Fatalf("SanitizeBranchName(%q) = %q, want %q", in, got, want)
		}
	}
}
