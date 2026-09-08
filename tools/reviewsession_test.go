package tools

import (
	"strings"
	"testing"

	"ai/forges"
)

// stubForge — минимальный Forge для проверки публикации замечаний.
type stubForge struct {
	posted []forges.ReviewComment
}

func (s *stubForge) GetDiff() (string, error) { return "", nil }
func (s *stubForge) PostComment(c forges.ReviewComment) error {
	s.posted = append(s.posted, c)
	return nil
}
func (s *stubForge) PostSummary(string) error { return nil }
func (s *stubForge) Approve(string) error     { return nil }

// Режим CriticalOnly отсекает несущественные замечания (стиль, «можно
// лучше», «стоит проверить»), оставляя только указания на реальные дефекты.
func TestFilterMinorCommentsWithCount(t *testing.T) {
	comments := []forges.ReviewComment{
		{FilePath: "a.go", Line: 1, Text: "критично: утечка ресурсов"},
		{FilePath: "a.go", Line: 2, Text: "баг: паника при пустом списке"}, // без маркера — реальный дефект, оставляем
		{FilePath: "a.go", Line: 3, Text: "для заметки: можно вынести в константу"},
		{FilePath: "a.go", Line: 4, Text: "стоит проверить, закрывается ли соединение"},
		{FilePath: "a.go", Line: 5, Text: "лучше использовать `empty()` вместо `!== []`"},
		{FilePath: "a.go", Line: 6, Text: "было бы удобнее переименовать переменную"},
	}

	kept, dropped := filterMinorCommentsWithCount(comments)
	if dropped != 4 {
		t.Fatalf("ожидали 4 отсечённых несущественных, got %d (kept=%d)", dropped, len(kept))
	}
	if len(kept) != 2 {
		t.Fatalf("ожидали 2 сохранённых замечания, got %d: %v", len(kept), kept)
	}
	if !IsCritical(kept[0].Text) {
		t.Errorf("критичное замечание должно быть сохранено, got %q", kept[0].Text)
	}
	if kept[1].Text != "баг: паника при пустом списке" {
		t.Errorf("дефект без маркера должен сохраняться, got %q", kept[1].Text)
	}
}

// Отсечение работает по всему тексту, включая регистр и пробелы.
func TestFilterMinorCommentsWithCountCaseInsensitive(t *testing.T) {
	comments := []forges.ReviewComment{
		{FilePath: "a.go", Line: 1, Text: " Стоит убедиться в правильности индексов "},
		{FilePath: "a.go", Line: 2, Text: "критично: SQL-инъекция"},
	}

	kept, dropped := filterMinorCommentsWithCount(comments)
	if dropped != 1 || len(kept) != 1 {
		t.Fatalf("dropped=%d kept=%d, ожидали 1 и 1", dropped, len(kept))
	}
}

// ReviewMr в режиме CriticalOnly публикует только дефекты, а не шум,
// и сообщает модели об отсечённых замечаниях.
func TestReviewMrCriticalOnly(t *testing.T) {
	ff := &stubForge{}
	ses := &ReviewSession{
		Forge:        ff,
		CriticalOnly: true,
		Diff: `+++ b/a.go
@@ -1,4 +1,4 @@
+line1
+line2
+line3
+line4`,
	}

	out, err := ses.ReviewMr(map[string]any{"comments": `[
		{"file_path":"a.go","line":1,"text":"критично: падение на пустом входе"},
		{"file_path":"a.go","line":2,"text":"для заметки: можно переименовать"},
		{"file_path":"a.go","line":3,"text":"стоит проверить таймауты"},
		{"file_path":"a.go","line":4,"text":"баг: результат игнорируется"}
	]`})
	if err != nil {
		t.Fatal(err)
	}

	if ses.CommentCount != 2 {
		t.Errorf("CommentCount = %d, ожидали 2 критические+дефектные", ses.CommentCount)
	}
	// "стоит проверить таймауты" уже отсекается нейрослоп-фильтром до нас;
	// на долю minor-фильтра приходится "для заметки: можно переименовать".
	if ses.MinorDroppedCount != 1 {
		t.Errorf("MinorDroppedCount = %d, ожидали 1", ses.MinorDroppedCount)
	}
	if !strings.Contains(string(out), "несущественных") {
		t.Errorf("модели должно сообщаться об отсечённых замечаниях, got %s", out)
	}
}
