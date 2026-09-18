package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGo(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestPublicAPISnapshotAndCompare(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "api.go", `package api
import "fmt"
// Фискальная функция.
func Calc(a int, b int) (int, error) { return a + b, nil }
func hidden() {}
type User struct {
	Name string
	Age  int
}
const DefaultLimit = 10
`)
	before, err := BuildPublicAPISnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range before.Entries {
		names[e.Kind+"/"+e.Name] = true
	}
	for _, want := range []string{"func/Calc", "type/User", "const/DefaultLimit"} {
		if !names[want] {
			t.Errorf("снимок не содержит %s, содержит %v", want, names)
		}
	}
	// Неэкспортированные и методы не должны попадать.
	if names["func/hidden"] {
		t.Error("приватная функция не должна попадать в снимок")
	}

	// Детеминизм: повторный снимок идентичен (никаких меток времени/порядка).
	again, err := BuildPublicAPISnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Entries) != len(before.Entries) {
		t.Fatalf("повторный снимок отличается: %d != %d", len(again.Entries), len(before.Entries))
	}

	// Изменяем код: добавляем функцию, меняем сигнатуру Calc, убираем константу.
	writeGo(t, dir, "api.go", `package api
import "fmt"
func Calc(a int, b int, extra string) (int, error) { return a + b, nil }
type User struct {
	Name string
	Age  int
}
func NewUser(name string) *User { return nil }
`)
	after, err := BuildPublicAPISnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	diff := ComparePublicAPI(before, after)
	joined := strings.Join(diff, "\n")
	if !strings.Contains(joined, "+ func NewUser") {
		t.Errorf("diff должен содержать добавленную функцию, got:\n%s", joined)
	}
	if !strings.Contains(joined, "- const DefaultLimit") {
		t.Errorf("diff должен содержать удалённую константу, got:\n%s", joined)
	}
	if !strings.Contains(joined, "~ func Calc") || !strings.Contains(joined, "было") {
		t.Errorf("diff должен отмечать изменение сигнатуры, got:\n%s", joined)
	}
	// Type User не менялся — его не должно быть в diff.
	if strings.Contains(joined, "type User") {
		t.Errorf("неизменённый тип не должен попадать в diff, got:\n%s", joined)
	}
}
