package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchGoFuncSourceReplacesOnlyNamedFunc(t *testing.T) {
	src := `package service

import "fmt"

func CreateUser(id int) error {
	return fmt.Errorf("old")
}

func DeleteUser(id int) error {
	return fmt.Errorf("del")
}
`
	out, err := PatchGoFuncSource("service.go", []byte(src), GoFuncPatchParams{
		TargetFile:   "service.go",
		FunctionName: "CreateUser",
		Body:         "func CreateUser(id int) error {\n\treturn fmt.Errorf(\"new\")\n}",
	})
	if err != nil {
		t.Fatalf("PatchGoFuncSource: %v", err)
	}
	got := string(out)
	for _, want := range []string{`fmt.Errorf("new")`, `fmt.Errorf("del")`} {
		if !strings.Contains(got, want) {
			t.Errorf("результат не содержит %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `fmt.Errorf("old")`) {
		t.Errorf("старое тело функции не заменено:\n%s", got)
	}
	if n := strings.Count(got, `import "fmt"`); n != 1 {
		t.Errorf("импорт fmt изменился, ожидался 1 экземпляр, got %d:\n%s", n, got)
	}
	if _, err := PatchGoFuncSource("service.go", out, GoFuncPatchParams{
		TargetFile: "service.go", FunctionName: "DeleteUser", Body: "func DeleteUser(id int) error { return nil }",
	}); err != nil {
		t.Errorf("повторный патч того же файла упал: %v", err)
	}
}

func TestPatchGoFuncSourceMethodByReceiver(t *testing.T) {
	src := `package service

type Service struct{}
type Client struct{}

func (s *Service) Name() string { return "service" }
func (c *Client) Name() string  { return "client" }
`
	out, err := PatchGoFuncSource("service.go", []byte(src), GoFuncPatchParams{
		TargetFile:   "service.go",
		FunctionName: "Name",
		Receiver:     "Service",
		Body:         "func (s *Service) Name() string { return \"patched\" }",
	})
	if err != nil {
		t.Fatalf("PatchGoFuncSource: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, `"patched"`) {
		t.Errorf("метод Service не заменён:\n%s", got)
	}
	if !strings.Contains(got, `"client"`) {
		t.Errorf("метод Client затронут патчем:\n%s", got)
	}
	if strings.Contains(got, `"service"`) {
		t.Errorf("старый метод Service остался:\n%s", got)
	}
}

func TestPatchGoFuncSourceAmbiguousWithoutReceiver(t *testing.T) {
	src := `package service

type Service struct{}
type Client struct{}

func (s *Service) Name() string { return "a" }
func (c *Client) Name() string  { return "b" }
`
	_, err := PatchGoFuncSource("service.go", []byte(src), GoFuncPatchParams{
		TargetFile:   "service.go",
		FunctionName: "Name",
		Body:         "func (s *Service) Name() string { return \"x\" }",
	})
	if err == nil || !strings.Contains(err.Error(), "receiver") {
		t.Fatalf("ожидалась ошибка про уточнение receiver, got: %v", err)
	}
}

func TestPatchGoFuncSourceNotFound(t *testing.T) {
	src := `package service

func CreateUser() {}
`
	_, err := PatchGoFuncSource("service.go", []byte(src), GoFuncPatchParams{
		TargetFile:   "service.go",
		FunctionName: "Missing",
		Body:         "func Missing() {}",
	})
	if err == nil || !strings.Contains(err.Error(), "не найдена") {
		t.Fatalf("ожидалась ошибка «не найдена», got: %v", err)
	}
}

func TestPatchGoFuncSourceNameMismatchRejected(t *testing.T) {
	src := `package service

func CreateUser() {}
`
	// body декларирует ДРУГУЮ функцию — защита от галлюцинаций модели.
	_, err := PatchGoFuncSource("service.go", []byte(src), GoFuncPatchParams{
		TargetFile:   "service.go",
		FunctionName: "CreateUser",
		Body:         "func DeleteUser() {}",
	})
	if err == nil || !strings.Contains(err.Error(), "function_name") {
		t.Fatalf("ожидалась ошибка о несовпадении имени, got: %v", err)
	}
}

func TestPatchGoFuncSourceReceiverChangedRejected(t *testing.T) {
	src := `package service

type Service struct{}

func (s *Service) Name() string { return "a" }
`
	// Метод нельзя тихо превратить в функцию — это сломало бы вызовы.
	_, err := PatchGoFuncSource("service.go", []byte(src), GoFuncPatchParams{
		TargetFile:   "service.go",
		FunctionName: "Name",
		Body:         "func Name() string { return \"b\" }",
	})
	if err == nil || !strings.Contains(err.Error(), "ресивер") {
		t.Fatalf("ожидалась ошибка про смену ресивера, got: %v", err)
	}
}

func TestPatchGoFuncSourceAddsImports(t *testing.T) {
	src := `package service

func CreateUser() {}
`
	out, err := PatchGoFuncSource("service.go", []byte(src), GoFuncPatchParams{
		TargetFile:   "service.go",
		FunctionName: "CreateUser",
		Body:         "func CreateUser() error { return errors.New(\"x\") }",
		Imports:      []string{"errors", "fmt"},
	})
	if err != nil {
		t.Fatalf("PatchGoFuncSource: %v", err)
	}
	got := string(out)
	for _, imp := range []string{`"errors"`, `"fmt"`} {
		if strings.Count(got, imp) != 1 {
			t.Errorf("импорт %s должен присутствовать ровно 1 раз:\n%s", imp, got)
		}
	}
	// Идемпотентность: повторное добавление того же импорта не дублирует его.
	out2, err := PatchGoFuncSource("service.go", out, GoFuncPatchParams{
		TargetFile:   "service.go",
		FunctionName: "CreateUser",
		Body:         "func CreateUser() error { return errors.New(\"y\") }",
		Imports:      []string{"errors"},
	})
	if err != nil {
		t.Fatalf("повторный патч: %v", err)
	}
	if strings.Count(string(out2), `"errors"`) != 1 {
		t.Errorf("импорт errors продублировался:\n%s", out2)
	}
}

func TestPatchGoFuncSourcePreservesDocComment(t *testing.T) {
	src := `package service

// CreateUser создаёт пользователя.
func CreateUser() {}
`
	out, err := PatchGoFuncSource("service.go", []byte(src), GoFuncPatchParams{
		TargetFile:   "service.go",
		FunctionName: "CreateUser",
		Body:         "func CreateUser() { println(1) }",
	})
	if err != nil {
		t.Fatalf("PatchGoFuncSource: %v", err)
	}
	if !strings.Contains(string(out), "// CreateUser создаёт пользователя.") {
		t.Errorf("doc-комментарий не сохранён:\n%s", out)
	}
}

func TestFileOpsPatchGoFunctionHandler(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	src := `package service

func CreateUser(id int) { _ = id }

func Keep() int { return 1 }
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := ops.PatchGoFunction(map[string]any{
		"target_file":   "service.go",
		"function_name": "CreateUser",
		"body":          "func CreateUser(id int) { println(id) }",
	})
	if err != nil {
		t.Fatalf("PatchGoFunction: %v", err)
	}
	if !strings.Contains(string(out), `"status":"success"`) {
		t.Fatalf("ожидался успех, got: %s", out)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "service.go"))
	if !strings.Contains(string(got), "println(id)") || !strings.Contains(string(got), "return 1") {
		t.Errorf("файл после патча повреждён:\n%s", got)
	}

	// Ошибка модели — функция не найдена: статус error, файл не меняется.
	before, _ := os.ReadFile(filepath.Join(dir, "service.go"))
	out, err = ops.PatchGoFunction(map[string]any{
		"target_file":   "service.go",
		"function_name": "NoSuch",
		"body":          "func NoSuch() {}",
	})
	if err != nil {
		t.Fatalf("ожидали статус-ошибку без err, got: %v", err)
	}
	if !strings.Contains(string(out), `"status":"error"`) {
		t.Fatalf("ожидался error, got: %s", out)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "service.go"))
	if string(before) != string(after) {
		t.Error("файл изменился при неудачном патче")
	}
}

func TestFileOpsPatchGoFunctionScopeRestriction(t *testing.T) {
	dir := t.TempDir()
	ops := &FileOps{OutputDir: dir}
	ops.SetScope([]string{"server"})
	if err := os.WriteFile(filepath.Join(dir, "api.go"), []byte("package api\n\nfunc F() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := ops.PatchGoFunction(map[string]any{
		"target_file":   "api.go",
		"function_name": "F",
		"body":          "func F() { println(1) }",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "вне области работы") {
		t.Fatalf("ожидалась ошибка scope, got: %s", out)
	}
}
