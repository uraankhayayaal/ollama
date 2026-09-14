package planner

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// effectiveCreationScope расширяет точечный scope ШАГА СОЗДАНИЯ до родительской
// директории: модель пишет файл под новым именем (напр. .js вместо .tsx из
// плана), иначе каждая запись заканчивается «вне области работы» и файл не
// появляется. Существующие файлы (шаги правки) не трогаем.
func TestEffectiveCreationScopeWidensNewFiles(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"lib/util.go", "frontend/src/App.tsx", "main.go"} {
		mustMkAll(t, dir, f)
	}

	cases := []struct {
		scope []string
		want  []string
	}{
		// Новые файлы → расширяем до родительских папок.
		{[]string{"frontend/src/index.tsx", "frontend/src/util.ts"}, []string{"frontend/src/", "frontend/src/"}},
		// Пиксельная правка существующего файла — scope без изменений.
		{[]string{"frontend/src/App.tsx"}, []string{"frontend/src/App.tsx"}},
		// Файл в корне (новый) остаётся файлом — нет родительской папки.
		{[]string{"main.go"}, []string{"main.go"}},
		// Прямые scope-записи (scope: "") не трогаем.
		{[]string{"frontend/src/"}, []string{"frontend/src/"}},
	}

	for i, c := range cases {
		got := effectiveCreationScope(dir, c.scope)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("case %d: effectiveCreationScope(%v) = %v, want %v", i, c.scope, got, c.want)
		}
	}
}

func mustMkAll(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

// leadStepScope: специалистов шага-декомпозиции лида нельзя сужать до точечного
// scope из плана (конкретных новых файлов) — лид раздаёт задачи на произвольные
// файлы области, и специалист упрётся в «вне области работы», не записав своё.
// Специалистам выдаётся корневая директория шага (frontend/, server/) и всегда
// доступен PLAN.md (там оркестратор ведёт план работ с контрактами).
func TestLeadStepScopeUsesTopLevelDir(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		scope []string
		want  []string
	}{
		// Точечный scope (новые файлы) → корневая директория области + PLAN.md.
		{[]string{"frontend/package.json"}, []string{"frontend/", "PLAN.md"}},
		{[]string{"server/internal/handler/rate.go", "server/internal/service/rate.go"}, []string{"server/", "PLAN.md"}},
		// Scope-директории — без изменения вышестоящей директории.
		{[]string{"server/"}, []string{"server/", "PLAN.md"}},
		{[]string{"frontend/"}, []string{"frontend/", "PLAN.md"}},
		// Пустой scope — только PLAN.md (план работ читается всем).
		{nil, []string{"PLAN.md"}},
		{[]string{}, []string{"PLAN.md"}},
	}

	for i, c := range cases {
		got := leadStepScope(dir, &Step{Scope: c.scope})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("case %d: leadStepScope(%v) = %v, want %v", i, c.scope, got, c.want)
		}
	}
}
