package gitops

// Гард объёма промежуточного коммита (Ф-6). Живой повод — mytrip/FEL-05:
// агент без npm в образе песочницы скачал Node в корень проекта, и
// `git add -A` закоммитал node.tar.gz (46 МБ) и 4287 файлов распакованного
// тулчейна под заголовком «wip: раунд 14 (файлов: 4)».

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initWipRepo — репозиторий на ветке задачи с одним коммитом, готовый к
// промежуточным коммитам. Настоящий git: гард меряет файлы на диске.
func initWipRepo(t *testing.T) *Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git недоступен")
	}
	dir := t.TempDir()
	rbRun(t, dir, "init", "-b", "main")
	rbRun(t, dir, "config", "user.email", "t@example.com")
	rbRun(t, dir, "config", "user.name", "Test")
	rbWrite(t, filepath.Join(dir, "a.go"), "package a\n")
	rbRun(t, dir, "add", "-A")
	rbRun(t, dir, "commit", "-m", "init")
	rbRun(t, dir, "checkout", "-b", TaskBranchPrefix+"T-1")
	return RepoFromState(&CLIExecutor{}, dir, "", "", "")
}

// TestCommitIfDirtyRejectsHugeArchive — скачанный архив не коммитится: иначе
// 46 МБ мусора уезжают в ветку задачи и дальше в MR.
func TestCommitIfDirtyRejectsHugeArchive(t *testing.T) {
	r := initWipRepo(t)
	big := make([]byte, 9<<20) // 9 МБ > лимита 8 МБ
	for i := range big {
		big[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(r.Root, "node.tar.gz"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	_, committed, err := r.CommitIfDirty(t.Context(), "wip: раунд 1 (файлов: 0)")
	if err == nil {
		t.Fatal("коммит с архивом 9 МБ должен быть отклонён")
	}
	if committed {
		t.Fatal("коммит не создан, но сообщено об успехе")
	}
	if !strings.Contains(err.Error(), "node.tar.gz") {
		t.Fatalf("ошибка должна называть виновника, got %v", err)
	}
	if !strings.Contains(err.Error(), "KANBAN_WIP_COMMIT_MAX_FILE_BYTES") {
		t.Fatalf("ошибка должна подсказывать, что настроить, got %v", err)
	}
	// Мусор остался в дереве, но НЕ в истории: откатываться есть куда.
	if out := rbRun(t, r.Root, "log", "--oneline"); strings.Contains(out, "wip: раунд 1") {
		t.Fatalf("коммит всё-таки создан:\n%s", out)
	}
	// Индекс очищен от half-staged состояния.
	if out := rbRun(t, r.Root, "status", "--porcelain"); !strings.Contains(out, "node.tar.gz") {
		t.Fatalf("мусор должен остаться untracked после отказа, status:\n%s", out)
	}
}

// TestCommitIfDirtyRejectsTooManySmallFiles — много мелких файлов (тулчейн без
// одного большого архива) ловится порогом общего объёма.
func TestCommitIfDirtyRejectsTooManySmallFiles(t *testing.T) {
	r := initWipRepo(t)
	chunk := make([]byte, 1<<20) // 1 МБ
	for i := range chunk {
		chunk[i] = byte(i)
	}
	for i := 0; i < 40; i++ { // 40 МБ > лимита 32 МБ, файлы меньше лимита 8 МБ
		name := filepath.Join(r.Root, "share", "man")
		if i == 0 {
			if err := os.MkdirAll(name, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(name, string(rune('a'+i%26))+strings.Repeat("x", i)), chunk, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, committed, err := r.CommitIfDirty(t.Context(), "wip: раунд 1")
	if err == nil || committed {
		t.Fatalf("набор на 40 МБ должен быть отклонён (err=%v committed=%v)", err, committed)
	}
	if !strings.Contains(err.Error(), "KANBAN_WIP_COMMIT_MAX_BYTES") {
		t.Fatalf("ошибка должна подсказывать порог общего объёма, got %v", err)
	}
}

// TestCommitIfDirtyAllowsNormalWork — обычная работа проходит: лимит не должен
// мешать ни новым файлам, ни удалениям.
func TestCommitIfDirtyAllowsNormalWork(t *testing.T) {
	r := initWipRepo(t)
	for _, f := range []string{"b.go", "c.go", "d.go"} {
		if err := os.WriteFile(filepath.Join(r.Root, f), []byte("package a // "+f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(r.Root, "a.go")); err != nil {
		t.Fatal(err)
	}
	sha, committed, err := r.CommitIfDirty(t.Context(), "wip: раунд 2")
	if err != nil || !committed || sha == "" {
		t.Fatalf("обычная работа должна коммититься: err=%v committed=%v sha=%q", err, committed, sha)
	}
	out := rbRun(t, r.Root, "show", "--stat", "--oneline", "HEAD")
	for _, want := range []string{"b.go", "c.go", "d.go", "a.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("в коммите нет %s:\n%s", want, out)
		}
	}
}

// TestCommitIfDirtyLimitsOverridable — проект с легальными большими бинарями
// поднимает порог окружением, а не ждёт обхода гарда.
func TestCommitIfDirtyLimitsOverridable(t *testing.T) {
	t.Setenv(wipMaxFileBytesEnv, "10485760") // 10 МБ
	r := initWipRepo(t)
	big := make([]byte, 9<<20)
	if err := os.WriteFile(filepath.Join(r.Root, "fixture.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, committed, err := r.CommitIfDirty(t.Context(), "wip: раунд 3")
	if err != nil || !committed || sha == "" {
		t.Fatalf("с поднятым лимитом файл 9 МБ должен коммититься: err=%v committed=%v", err, committed)
	}
}

// TestStagedLimitsEnvParsing — мусор и неположительные значения не ломают
// гард: используется дефолт.
func TestStagedLimitsEnvParsing(t *testing.T) {
	cases := []struct {
		env  string
		want int64
	}{
		{"", 8 << 20},
		{"1024", 1024},
		{"0", 8 << 20},
		{"-5", 8 << 20},
		{"мусор", 8 << 20},
	}
	for _, c := range cases {
		t.Setenv(wipMaxFileBytesEnv, c.env)
		if got := wipMaxFileBytes(); got != c.want {
			t.Fatalf("%s=%q → %d, ожидалось %d", wipMaxFileBytesEnv, c.env, got, c.want)
		}
	}
}
