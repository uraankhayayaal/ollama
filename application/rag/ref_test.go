// Тесты сбора RAG-снапшотов ветки из git-объектов (application/rag/ref.go):
// разбор ls-tree, отсев бинарных/служебных файлов и сбор изменений ветки.
package rag

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// refGit — hermetic-исполнитель git для сбора ветки: ls-tree отдаёт готовый
// вывод, show/rev-parse — заранее заданные значения по точному аргументу.
type refGit struct {
	mu      sync.Mutex
	tree    string
	diff    string            // вывод `git diff --name-only -z base...ref`
	commits map[string]string // ref → sha
	sizes   map[string]int64  // "ref:path" → размер blob-а
	shows   map[string]string // "ref:path" → содержимое
	failLS  string
}

func (g *refGit) Exec(_ context.Context, _ string, argv ...string) (string, error) {
	cmd := strings.Join(argv, " ")
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case strings.HasPrefix(cmd, "git ls-tree"):
		if g.failLS != "" {
			return "", errors.New(g.failLS)
		}
		return g.tree, nil
	case strings.HasPrefix(cmd, "git diff"):
		return g.diff, nil
	case strings.HasPrefix(cmd, "git cat-file"):
		size, ok := g.sizes[argv[len(argv)-1]]
		if !ok {
			return "", fmt.Errorf("нет такого пути в ветке: %s", argv[len(argv)-1])
		}
		return strconv.FormatInt(size, 10), nil
	case strings.HasPrefix(cmd, "git rev-parse"):
		return g.commits[argv[len(argv)-1]], nil
	case strings.HasPrefix(cmd, "git show"):
		out, ok := g.shows[argv[len(argv)-1]]
		if !ok {
			return "", fmt.Errorf("нет такого пути в ветке: %s", argv[len(argv)-1])
		}
		return out, nil
	}
	return "", nil
}

// lsTreeEntry собирает строку `git ls-tree --long` для blob-а.
func lsTreeEntry(mode, sha string, size int, path string) string {
	return fmt.Sprintf("%s blob %s %7d\t%s\x00", mode, sha, size, path)
}

func TestLoadRefItemsParsesAndFilters(t *testing.T) {
	binary := "PK\x03\x04\x00bin"
	tree := strings.Join([]string{
		lsTreeEntry("100644", "a1", 12, "main.go"),
		lsTreeEntry("100644", "a2", 20, "server/handler.go"),
		// Скрытый файл и служебные каталоги — как и при обходе рабочей копии.
		lsTreeEntry("100644", "a3", 5, ".env"),
		lsTreeEntry("100644", "a4", 9, ".github/workflows/ci.yml"),
		lsTreeEntry("100644", "a5", 9, "node_modules/pkg/index.js"),
		lsTreeEntry("100644", "a6", 9, "web/dist/bundle.js"),
		// Симлинк и поддерево не индексируем.
		lsTreeEntry("120000", "a7", 4, "link.txt"),
		"040000 tree a8       -\tdir\x00",
		// Пустой и крупный файл — заранее отсекаются по размеру из ls-tree.
		lsTreeEntry("100644", "a9", 0, "empty.txt"),
		lsTreeEntry("100644", "b0", MaxRefFileBytes+1, "vendor/big.js"),
	}, "")

	git := &refGit{
		tree: tree,
		commits: map[string]string{
			"main": "c0ffee1234567890abcdef1234567890abcdef12\n",
		},
		shows: map[string]string{
			"main:main.go":             "package main\n",
			"main:server/handler.go":   "package server\n",
			"main:.env":                "TOKEN=1\n",
			"main:.github/workflows/c": "",
			"main:link.txt":            "main.go",
		},
	}

	snap, err := LoadRefItems(context.Background(), git, "/repo", "main")
	if err != nil {
		t.Fatalf("LoadRefItems: %v", err)
	}
	if snap.Branch != "main" {
		t.Fatalf("ветка снапшота: %q, want main", snap.Branch)
	}
	if snap.CommitSHA != "c0ffee1234567890abcdef1234567890abcdef12" {
		t.Fatalf("коммит снапшота: %q", snap.CommitSHA)
	}
	if len(snap.Items) != 2 {
		t.Fatalf("файлов в снапшоте %d (%+v), want 2: main.go и server/handler.go", len(snap.Items), snap.Items)
	}
	if snap.Items[0].Path != "main.go" || snap.Items[0].Scope != "root" {
		t.Fatalf("первый файл: %+v", snap.Items[0])
	}
	if snap.Items[1].Path != "server/handler.go" || snap.Items[1].Scope != "server" {
		t.Fatalf("второй файл: %+v", snap.Items[1])
	}
	for _, it := range snap.Items {
		if strings.ContainsRune(it.Content, 0) {
			t.Fatalf("бинарное содержимое попало в индекс: %s", it.Path)
		}
	}
	_ = binary // бинарный отсев проверяется через isBinary-предикат отдельно
}

func TestLoadRefItemsBinaryAndErrors(t *testing.T) {
	git := &refGit{
		tree: strings.Join([]string{
			lsTreeEntry("100644", "a1", 8, "logo.png"),
			lsTreeEntry("100644", "a2", 6, "readme.md"),
		}, ""),
		shows: map[string]string{
			"main:logo.png":  "PK\x03\x04\x00\x01",
			"main:readme.md": "# проект\n",
		},
	}
	snap, err := LoadRefItems(context.Background(), git, "/repo", "main")
	if err != nil {
		t.Fatalf("LoadRefItems: %v", err)
	}
	if len(snap.Items) != 1 || snap.Items[0].Path != "readme.md" {
		t.Fatalf("бинарный файл не отсеян: %+v", snap.Items)
	}

	failing := &refGit{failLS: "unknown revision"}
	if _, err := LoadRefItems(context.Background(), failing, "/repo", "main"); err == nil {
		t.Fatal("ошибка git ls-tree должна пробрасываться (нет молчаливого пустого индекса)")
	}
	if _, err := LoadRefItems(context.Background(), nil, "/repo", "main"); err == nil {
		t.Fatal("без git-исполнителя — ошибка")
	}
	if _, err := LoadRefItems(context.Background(), git, "", "main"); err == nil {
		t.Fatal("без каталога — ошибка")
	}
}

// TestLoadRefDiffOnlyChanged — индексация ветки задачи берёт только изменения
// относительно базы (`git diff base...ref`): остальное покрывает индекс main.
// Удалённые файлы и мусорные пути отсеиваются.
func TestLoadRefDiffOnlyChanged(t *testing.T) {
	git := &refGit{
		diff:    "server/handler.go\x00README.md\x00node_modules/pkg/i.js\x00old/gone.go\x00",
		commits: map[string]string{"ai/task/T-01": "b0b0b0b0\n"},
		sizes:   map[string]int64{"ai/task/T-01:server/handler.go": 20, "ai/task/T-01:README.md": 8, "ai/task/T-01:old/gone.go": 4},
		shows: map[string]string{
			"ai/task/T-01:server/handler.go": "package server\n",
			"ai/task/T-01:README.md":         "# проект\n",
		},
	}
	snap, err := LoadRefDiff(context.Background(), git, "/repo", "ai/task/T-01", "main")
	if err != nil {
		t.Fatalf("LoadRefDiff: %v", err)
	}
	if snap.Branch != "ai/task/T-01" || snap.CommitSHA != "b0b0b0b0" {
		t.Fatalf("снапшот: %+v", snap)
	}
	got := make([]string, 0, len(snap.Items))
	for _, it := range snap.Items {
		got = append(got, it.Path)
	}
	want := []string{"server/handler.go", "README.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("файлы изменений = %v, want %v", got, want)
	}

	if _, err := LoadRefDiff(context.Background(), git, "/repo", "", "main"); err == nil {
		t.Fatal("без ветки — ошибка")
	}
	if _, err := LoadRefDiff(context.Background(), git, "/repo", "ai/task/T-01", ""); err == nil {
		t.Fatal("без базовой ветки — ошибка")
	}
}
