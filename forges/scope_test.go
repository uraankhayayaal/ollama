package forges

import "testing"

func TestScopeMatcherAllow(t *testing.T) {
	m := CompileScope([]string{"src/main.go", "internal/order/"})

	cases := []struct {
		rel  string
		want bool
	}{
		{"src/main.go", true},       // точное совпадение с файлом
		{"internal/order/repo.go", true}, // файл внутри директории
		{"internal/order", true},     // сама директория
		{"internal/order/sub/x.go", true}, // вложенный файл
		{"src/other.go", false},      // соседний файл вне области
		{"internal/misc.go", false},  // в другой папке internal
		{"internal/handlers.go", false},
		{"go.mod", false},
	}

	for _, c := range cases {
		if got := m.Allow(c.rel); got != c.want {
			t.Errorf("Allow(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

func TestScopeMatcherEmptyAllowed(t *testing.T) {
	m := CompileScope(nil)
	if !m.Empty() {
		t.Fatal("CompileScope(nil) должен быть Empty")
	}
	if !m.Allow("anything.go") {
		t.Fatal("без области всё должно быть разрешено")
	}
}

func TestScopeMatcherNormalizesEntries(t *testing.T) {
	// Ведущие/хвостовые слэши, ./ и мусор не должны ломать матчинг.
	m := CompileScope([]string{"./", " /src/ ", "internal//order/", ""})
	if !m.Allow("src/foo.go") {
		t.Fatal("norm 'src' должен разрешать src/foo.go")
	}
	if !m.Allow("internal/order/repo.go") {
		t.Fatal("norm 'internal/order' должен разрешать внутренние файлы")
	}
	if m.Allow("src.go") {
		t.Fatal("src.go — соседний файл директории src/, должен быть вне области")
	}
}

func TestScopeMatcherHasInside(t *testing.T) {
	m := CompileScope([]string{"internal/order/repo.go"})

	if !m.HasInside("internal") {
		t.Fatal("нужно заходить в internal — там есть лист из области")
	}
	if !m.HasInside("internal/order") {
		t.Fatal("нужно заходить в internal/order — там файл из области")
	}
	if m.HasInside("src") {
		t.Fatal("в src ничего из области нет — заходить не нужно")
	}
	if m.HasInside("internal/misc") {
		t.Fatal("internal/misc вне области — заходить не нужно")
	}
}