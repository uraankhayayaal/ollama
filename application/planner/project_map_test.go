package planner

import (
	"ai/projects"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildProjectMapExisting(t *testing.T) {
	name := "testmap_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	dir := projects.ProjectDir(name)
	if err := os.MkdirAll(filepath.Join(dir, "internal", "order"), 0755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	files := map[string]string{
		"main.go":                 "package main\n\nfunc main() {}\n",
		"internal/order/model.go": "package order\n\ntype Model struct{}\n",
		"go.mod":                  "module test\n",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Мусор, который не должен попасть в карту.
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "dep"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "dep", "x.js"), []byte("// lib"), 0644); err != nil {
		t.Fatal(err)
	}

	m := BuildProjectMap(name)

	for _, want := range []string{"main.go", "internal/order/model.go", "go.mod", "Языки по расширениям", ".go=2"} {
		if !strings.Contains(m, want) {
			t.Errorf("карта проекта не содержит %q:\n%s", want, m)
		}
	}
	if strings.Contains(m, "node_modules") || strings.Contains(m, "x.js") {
		t.Errorf("карта не должна содержать исключённые директории:\n%s", m)
	}
}

func TestBuildProjectMapMissing(t *testing.T) {
	name := "testmap_missing_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	m := BuildProjectMap(name)
	if !strings.Contains(m, "ещё не существует") {
		t.Errorf("для отсутствующего проекта ожидали пометку, got:\n%s", m)
	}
}
