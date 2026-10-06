package sandbox

import (
	"context"
	"strings"
	"testing"
)

// Run обязан отбраковывать разрушительные команды ДО любого обращения к
// Docker: даже с nil-движком (недоступный демон) падать должно на политике,
// а не на nil-указателе.
func TestDockerRunBlocksDestructiveBeforeEngine(t *testing.T) {
	w := &DockerWorkspace{project: "p", dir: "/tmp/p"}
	_, err := w.Run(context.Background(), "/tmp/p", "rm -rf /")
	if err == nil || !strings.Contains(err.Error(), "политик") {
		t.Errorf("ожидался отказ политики, получено %v", err)
	}
}

// Каталог вне смонтированных областей сессии — ошибка до обращения к демону
// (иначе nil-движок дал бы невнятный nil-panic вместо диагноза).
func TestDockerRunRejectsForeignWorkdir(t *testing.T) {
	w := &DockerWorkspace{project: "p", dir: "/tmp/p", mounts: []string{"/tmp/p"}}
	if _, err := w.Run(context.Background(), "/etc", "id"); err == nil ||
		!strings.Contains(err.Error(), "вне смонтированных") {
		t.Errorf("ожидался отказ по покрытию, получено %v", err)
	}
	// Несуществующий каталог — тоже до демона.
	if _, err := w.Run(context.Background(), "/tmp/p/nope", "id"); err == nil ||
		!strings.Contains(err.Error(), "не найден") {
		t.Errorf("ожидалась ошибка каталога, получено %v", err)
	}
}

func TestContainerName(t *testing.T) {
	if got := containerName("my-trip"); got != "ai-sandbox-my-trip" {
		t.Errorf("containerName = %q", got)
	}
	// Недопустимые для docker символы заменяются; полностью вымаранное имя
	// не может потерять префикс (иначе коллизии имён).
	if got := containerName("Проект/имя"); got != "ai-sandbox-project" {
		t.Errorf("санитайзинг имени: %q", got)
	}
	if containerName("My.Project_1") != "ai-sandbox-my.project_1" {
		t.Errorf("регистр должен опускаться: %q", containerName("My.Project_1"))
	}
}

func TestBindsMountSamePathReadWrite(t *testing.T) {
	w := &DockerWorkspace{mounts: []string{"/tmp/p", "/tmp/p/.wt-task-p-1"}}
	got := w.binds()
	want := []string{"/tmp/p:/tmp/p", "/tmp/p/.wt-task-p-1:/tmp/p/.wt-task-p-1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("binds = %v, ожидалось %v", got, want)
	}
	w.cfg.WorkdirReadOnly = true
	if b := w.binds(); !strings.HasSuffix(b[0], ":ro") {
		t.Errorf("WorkdirReadOnly должен дать :ro: %v", b)
	}
}

func TestAppendUniqueDirDropsNested(t *testing.T) {
	got := appendUniqueDir(nil, "/tmp/p")
	got = appendUniqueDir(got, "/tmp/p/sub") // вложенность — уже покрыта
	if len(got) != 1 || got[0] != "/tmp/p" {
		t.Errorf("вложенность не должна добавляться: %v", got)
	}
	got = appendUniqueDir([]string{"/tmp/p/sub"}, "/tmp/p") // новый покрывает старый
	if len(got) != 1 || got[0] != "/tmp/p" {
		t.Errorf("короткий путь должен заменить вложенный: %v", got)
	}
}

func TestParseLimits(t *testing.T) {
	if got := parseBytesLimit("2g"); got != 2<<30 {
		t.Errorf("2g = %d", got)
	}
	if got := parseBytesLimit("512m"); got != 512<<20 {
		t.Errorf("512m = %d", got)
	}
	if got := parseBytesLimit("некорректно"); got != 0 {
		t.Errorf("мусор должен дать 0 (без лимита), получено %d", got)
	}
	if got := parseCPULimit("1.5"); got != 1_500_000_000 {
		t.Errorf("1.5 = %d", got)
	}
	if got := parseCPULimit(""); got != 0 {
		t.Errorf("пусто = 0, получено %d", got)
	}
}

func TestSortedEnvIsDeterministic(t *testing.T) {
	a := sortedEnv(map[string]string{"B": "2", "A": "1"})
	b := sortedEnv(map[string]string{"A": "1", "B": "2"})
	if strings.Join(a, ",") != "A=1,B=2" || strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("env обязан сортироваться: %v vs %v", a, b)
	}
}

// Обёртка timeout: только когда в образе он есть и дедлайн задан.
// Фактический argv собирается в timeoutArgv — Run его отдаёт в движок.
func TestRunWrapsTimeoutWhenAvailable(t *testing.T) {
	if got := timeoutArgv(false, 30, "go test ./..."); strings.Join(got, " ") != "sh -c go test ./..." {
		t.Errorf("без timeout в образе: %v", got)
	}
	if got := timeoutArgv(true, 30, "go test ./..."); strings.Join(got, " ") !=
		"timeout -s KILL 30 sh -c go test ./..." {
		t.Errorf("с timeout: %v", got)
	}
	// Нет дедлайна — таймер не ставим: некому его отменять.
	if got := timeoutArgv(true, 0, "sleep 1"); strings.Join(got, " ") != "sh -c sleep 1" {
		t.Errorf("без дедлайна: %v", got)
	}
}
