// Тесты конкретных фоновых прогонов индексации (application/rag/jobs.go):
// содержимое ветки, изменения ветки относительно базы и произвольный каталог,
// пропуск актуального индекса. Загрузчики и индексатор подменяются заглушками.
package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai/infrastructure/git"
	infrarag "ai/infrastructure/rag"
)

func snapRef(branch, commit string, items int) RefSnapshot {
	files := make([]infrarag.IndexItem, items)
	for i := range files {
		files[i] = infrarag.IndexItem{Path: "f.go", Content: "x"}
	}
	return RefSnapshot{Branch: branch, CommitSHA: commit, Items: files}
}

func refLoader(snap RefSnapshot, err error) RefLoader {
	return func(context.Context, git.Executor, string, string) (RefSnapshot, error) {
		return snap, err
	}
}

func diffLoader(snap RefSnapshot, err error) DiffLoader {
	return func(context.Context, git.Executor, string, string, string) (RefSnapshot, error) {
		return snap, err
	}
}

func TestRefJobHermetic(t *testing.T) {
	idx := &fakeProjectIndexer{}
	sink := &recordingSink{}
	job := RefJob{Project: "p", Dir: "/proj", Loader: refLoader(snapRef("main", "c1", 2), nil), Sink: sink}

	res, branch, skip, err := job.Run(context.Background(), idx, "main")
	if err != nil || skip {
		t.Fatalf("err=%v skip=%v", err, skip)
	}
	if res.Files != 2 || branch != "main" {
		t.Fatalf("res=%+v branch=%q", res, branch)
	}
	if idx.ensured != 1 || len(idx.calls) != 1 {
		t.Fatalf("ожидалась индексация: ensured=%d calls=%d", idx.ensured, len(idx.calls))
	}
	if idx.calls[0].opts.Branch != "main" || idx.calls[0].opts.CommitSHA != "c1" {
		t.Fatalf("опции индексации: %+v", idx.calls[0].opts)
	}
	if len(sink.info) != 1 || !strings.Contains(sink.info[0], "ветка main из git") {
		t.Fatalf("info о прогоне: %v", sink.info)
	}
}

func TestRefJobSkipsUpToDate(t *testing.T) {
	idx := &fakeProjectIndexer{indexed: true}
	sink := &recordingSink{}
	job := RefJob{Project: "p", Dir: "/proj", Loader: refLoader(snapRef("main", "c1", 2), nil), Sink: sink}

	_, _, skip, err := job.Run(context.Background(), idx, "main")
	if err != nil || !skip {
		t.Fatalf("актуальный индекс: err=%v skip=%v", err, skip)
	}
	if idx.ensured != 0 || len(idx.calls) != 0 {
		t.Fatalf("при актуальном индексе индексация не запускается: ensured=%d calls=%d", idx.ensured, len(idx.calls))
	}
	if len(sink.info) != 0 {
		t.Fatalf("info не должно быть: %v", sink.info)
	}
}

func TestRefJobLoaderError(t *testing.T) {
	idx := &fakeProjectIndexer{}
	job := RefJob{Project: "p", Dir: "/proj", Loader: refLoader(RefSnapshot{}, errors.New("boom")), Sink: &recordingSink{}}

	res, _, skip, err := job.Run(context.Background(), idx, "main")
	if err == nil || skip || res != nil {
		t.Fatalf("ошибка загрузчика должна возвращаться: err=%v res=%v", err, res)
	}
}

func TestRefDiffJobHermetic(t *testing.T) {
	idx := &fakeProjectIndexer{}
	sink := &recordingSink{}
	job := RefDiffJob{Project: "p", Dir: "/proj", Base: "main", Loader: diffLoader(snapRef("ai/task/T-1", "c2", 1), nil), Sink: sink}

	res, branch, skip, err := job.Run(context.Background(), idx, "ai/task/T-1")
	if err != nil || skip || res.Files != 1 {
		t.Fatalf("err=%v skip=%v res=%+v", err, skip, res)
	}
	if branch != "ai/task/T-1" {
		t.Fatalf("branch=%q", branch)
	}
	if len(sink.info) != 1 || !strings.Contains(sink.info[0], "изменения ветки ai/task/T-1 относительно main") {
		t.Fatalf("info о прогоне: %v", sink.info)
	}
}

func TestDirJobHermetic(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "main.go", "package main\n")
	writeTestFile(t, dir, "web/index.ts", "const x = 1\n")

	idx := &fakeProjectIndexer{}
	sink := &recordingSink{}
	job := DirJob{Project: "p", Dir: dir, What: "проект", Opts: infrarag.IndexOptions{Branch: "main", CommitSHA: "c3"}, Sink: sink}

	res, branch, skip, err := job.Run(context.Background(), idx)
	if err != nil || skip || res.Files != 2 {
		t.Fatalf("err=%v skip=%v res=%+v", err, skip, res)
	}
	if branch != "main" {
		t.Fatalf("branch=%q", branch)
	}
	if len(sink.info) != 1 || !strings.Contains(sink.info[0], "обход") {
		t.Fatalf("info о прогоне: %v", sink.info)
	}
}
