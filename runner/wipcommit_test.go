package runner

// Тесты промежуточных коммитов (Ф-6, PLAN-2026-10-05-todo-kanban-rollback.md,
// этап 1): сообщение коммита и env-флаг. Сам хук в runner.go покрывается
// существующими тестами цикла раундов (он не меняет управляющий поток).

import (
	"strings"
	"testing"
)

func TestWipCommitEnabledDefaultOn(t *testing.T) {
	t.Setenv(wipCommitOnEnv, "")
	if !wipCommitEnabled() {
		t.Fatal("по умолчанию промежуточные коммиты должны быть включены")
	}
}

func TestWipCommitEnabledExplicitOff(t *testing.T) {
	for _, v := range []string{"0", "false", "off", "no", "FALSE", " Off "} {
		t.Setenv(wipCommitOnEnv, v)
		if wipCommitEnabled() {
			t.Fatalf("KANBAN_WIP_COMMIT=%q должен выключать коммиты", v)
		}
	}
}

func TestWipCommitEnabledExplicitOn(t *testing.T) {
	for _, v := range []string{"1", "true", "on", "yes"} {
		t.Setenv(wipCommitOnEnv, v)
		if !wipCommitEnabled() {
			t.Fatalf("KANBAN_WIP_COMMIT=%q должен включать коммиты", v)
		}
	}
}

func TestWipRoundMessageMentionsRoundAndFiles(t *testing.T) {
	msg := WipRoundMessage(3, []string{"server/a.go", "server/b.go"})
	if !strings.Contains(msg, "раунд 3") {
		t.Fatalf("нет номера раунда: %q", msg)
	}
	if !strings.Contains(msg, "файлов: 2") {
		t.Fatalf("нет числа файлов: %q", msg)
	}
	for _, f := range []string{"server/a.go", "server/b.go"} {
		if !strings.Contains(msg, f) {
			t.Fatalf("нет файла %s: %q", f, msg)
		}
	}
}

func TestWipRoundMessageTruncatesLongList(t *testing.T) {
	files := make([]string, 0, wipCommitFilesMax+5)
	for i := 0; i < wipCommitFilesMax+5; i++ {
		files = append(files, "f"+strings.Repeat("x", 3)+string(rune('a'+i%26))+".go")
	}
	msg := WipRoundMessage(1, files)
	if !strings.Contains(msg, "ещё 5") {
		t.Fatalf("нет счётчика остатка: %q", msg)
	}
	// Усечённый хвост не должен попасть в сообщение целиком.
	if strings.Count(msg, ".go") > wipCommitFilesMax {
		t.Fatalf("список не усечён: %q", msg)
	}
}

func TestWipRoundMessageWithoutFiles(t *testing.T) {
	msg := WipRoundMessage(7, nil)
	if !strings.Contains(msg, "раунд 7") {
		t.Fatalf("нет номера раунда: %q", msg)
	}
	if strings.Contains(msg, "файлов:") {
		t.Fatalf("пустой список не должен упоминаться: %q", msg)
	}
}
