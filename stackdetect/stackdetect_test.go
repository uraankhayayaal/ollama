package stackdetect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectKind(t *testing.T) {
	dir := t.TempDir()

	writeMarker(t, dir, "go.mod")
	if got := DetectKind(dir); got != KindGo {
		t.Fatalf("go.mod: got %q, want %q", got, KindGo)
	}
	os.Remove(filepath.Join(dir, "go.mod"))

	writeMarker(t, dir, "package.json")
	if got := DetectKind(dir); got != KindNode {
		t.Fatalf("package.json: got %q, want %q", got, KindNode)
	}
	os.Remove(filepath.Join(dir, "package.json"))

	writeMarker(t, dir, "composer.json")
	if got := DetectKind(dir); got != KindPhp {
		t.Fatalf("composer.json: got %q, want %q", got, KindPhp)
	}
	os.Remove(filepath.Join(dir, "composer.json"))

	writeMarker(t, dir, "requirements.txt")
	if got := DetectKind(dir); got != KindPython {
		t.Fatalf("requirements.txt: got %q, want %q", got, KindPython)
	}
	os.Remove(filepath.Join(dir, "requirements.txt"))

	writeMarker(t, dir, "pyproject.toml")
	if got := DetectKind(dir); got != KindPython {
		t.Fatalf("pyproject.toml: got %q, want %q", got, KindPython)
	}
	os.Remove(filepath.Join(dir, "pyproject.toml"))

	for _, marker := range []string{"setup.py", "main.py", "app.py"} {
		writeMarker(t, dir, marker)
		if got := DetectKind(dir); got != KindPython {
			t.Fatalf("%s: got %q, want %q", marker, got, KindPython)
		}
		os.Remove(filepath.Join(dir, marker))
	}

	if got := DetectKind(dir); got != KindUnknown {
		t.Fatalf("empty dir: got %q, want %q", got, KindUnknown)
	}
}

func TestDetectKindPrefersGoMod(t *testing.T) {
	dir := t.TempDir()
	// go.mod имеет приоритет даже при наличии package.json.
	writeMarker(t, dir, "go.mod")
	writeMarker(t, dir, "package.json")
	if got := DetectKind(dir); got != KindGo {
		t.Fatalf("got %q, want %q", got, KindGo)
	}
}

func TestDetectKindPrefersComposerOverPackageJSON(t *testing.T) {
	// Laravel и пакетные PHP-проекты несут и composer.json, и package.json:
	// корень трактуется как PHP-стек (composer/php -l/artisan корректнее,
	// чем Node-сборка фронтенд-ассетов).
	dir := t.TempDir()
	writeMarker(t, dir, "composer.json")
	writeMarker(t, dir, "package.json")
	if got := DetectKind(dir); got != KindPhp {
		t.Fatalf("got %q, want %q", got, KindPhp)
	}
}

func TestHasFile(t *testing.T) {
	dir := t.TempDir()
	writeMarker(t, dir, "file.go")
	if !HasFile(dir, "file.go") {
		t.Fatal("file.go должен существовать")
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if HasFile(dir, "subdir") {
		t.Fatal("директория не считается обычным файлом")
	}
	if HasFile(dir, "missing.go") {
		t.Fatal("missing.go не должен существовать")
	}
}

func writeMarker(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("marker\n"), 0644); err != nil {
		t.Fatal(err)
	}
}
