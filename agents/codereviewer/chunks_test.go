package codereviewer

import (
	"strings"
	"testing"

	"ai/forges"
)

// diff с двумя файлами, чтобы проверить разбиение по границам файлов.
const twoFilesDiff = `diff --git a/a.go b/a.go
+++ b/a.go
@@ -1,1 +1,1 @@
+aaa
diff --git a/b.go b/b.go
+++ b/b.go
@@ -1,1 +1,2 @@
+bbb
+ccc
`

func TestSplitDiffChunksWithinLimit(t *testing.T) {
	chunks := splitDiffChunks(twoFilesDiff, 10000)
	if len(chunks) != 1 {
		t.Fatalf("дифф меньше лимита должен дать 1 чанк, got %d", len(chunks))
	}
	if chunks[0] != twoFilesDiff {
		t.Error("одиночный чанк должен совпадать с исходным диффом")
	}
}

func TestSplitDiffChunksByFile(t *testing.T) {
	// Лимит меньше размера диффа — должно разбиться на 2 чанка по файлам.
	chunks := splitDiffChunks(twoFilesDiff, 10)
	if len(chunks) != 2 {
		t.Fatalf("ожидали 2 чанка (по файлам), got %d: %#v", len(chunks), chunks)
	}
	if !strings.Contains(chunks[0], "a.go") || strings.Contains(chunks[0], "b.go") {
		t.Errorf("чанк 0 должен содержать только a.go, got: %q", chunks[0])
	}
	// Восстановленный из чанков дифф должен сохранить весь контент.
	joined := strings.Join(chunks, "\n")
	for _, want := range []string{"aaa", "bbb", "ccc"} {
		if !strings.Contains(joined, want) {
			t.Errorf("чанки потеряли содержимое %q", want)
		}
	}
}

func TestSplitDiffChunksDisableWithZero(t *testing.T) {
	chunks := splitDiffChunks(twoFilesDiff, 0)
	if len(chunks) != 1 {
		t.Fatalf("ChunkSize=0 должен отключить разбиение, got %d", len(chunks))
	}
}

func TestNextChunkFlow(t *testing.T) {
	cr := &Codereviewer{
		chunks:   []string{"chunk1", "chunk2"},
		chunkIdx: 0,
	}
	// Первый NextChunk отдаёт второй чанк.
	r1 := cr.NextChunk()
	if !strings.Contains(string(r1), "chunk2") {
		t.Errorf("первый NextChunk должен вернуть chunk2, got %s", r1)
	}
	if cr.chunkIdx != 1 {
		t.Errorf("chunkIdx = %d, ожидали 1", cr.chunkIdx)
	}
	// Второй NextChunk (последний) сообщает о завершении.
	r2 := cr.NextChunk()
	if !strings.Contains(string(r2), "done") {
		t.Errorf("последний NextChunk должен сообщить о завершении, got %s", r2)
	}
	if cr.chunkIdx != 1 {
		t.Errorf("chunkIdx не должен сдвигаться после последнего чанка, got %d", cr.chunkIdx)
	}
}

func TestDedupCommentsByLocation(t *testing.T) {
	in := []forges.ReviewComment{
		{FilePath: "a.go", Line: 1, Text: "Проверь nil"},
		{FilePath: "a.go", Line: 1, Text: "проверь nil"}, // та же локация, другой регистр — дубль
		{FilePath: "a.go", Line: 2, Text: "Проверь nil"}, // та же проблема, другая строка — сохраняется
		{FilePath: "a.go", Line: 3, Text: "критично: утечка памяти"},
		{FilePath: "a.go", Line: 4, Text: "утечка памяти"}, // та же мысль, другая строка — сохраняется
	}

	out := dedupComments(in, map[string]bool{})

	lines := []int{}
	for _, c := range out {
		lines = append(lines, c.Line)
	}

	// Должны остаться строки 1,2,3,4 (удаляется только дубль строки 1).
	want := map[int]bool{1: true, 2: true, 3: true, 4: true}
	got := map[int]bool{}
	for _, l := range lines {
		got[l] = true
	}
	if !equalBoolMaps(want, got) {
		t.Errorf("ожидали строки {1,2,3,4}, got %v", got)
	}
	// Дубликат той же локации (строка 1) должен быть отброшен.
	if len(out) != 4 {
		t.Errorf("ожидали 4 замечания после дедупа по локации, got %d: %v", len(out), out)
	}
}

func TestDedupCommentsPersistsAcrossBatches(t *testing.T) {
	seen := map[string]bool{}

	// Первый раунд публикует замечание на a.go:1.
	first := dedupComments([]forges.ReviewComment{
		{FilePath: "a.go", Line: 1, Text: "Проверь nil"},
	}, seen)
	if len(first) != 1 {
		t.Fatalf("первый раунд: ожидали 1 замечание, got %d", len(first))
	}

	// Второй раунд снова предлагает то же замечание — оно должно быть
	// отброшено, т.к. локация уже опубликована.
	second := dedupComments([]forges.ReviewComment{
		{FilePath: "a.go", Line: 1, Text: "Проверь nil"},
		{FilePath: "b.go", Line: 7, Text: "новое замечание"},
	}, seen)
	if len(second) != 1 || second[0].FilePath != "b.go" {
		t.Errorf("второй раунд: ожидали только новое замечание b.go:7, got %#v", second)
	}
}

func equalBoolMaps(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
