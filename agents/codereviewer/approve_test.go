package codereviewer

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ai/forges"
)

// fakeForge: PostComment можно заставить падать, Approve фиксирует вызовы.
type fakeForge struct {
	failPost bool
	approves int
}

func (f *fakeForge) GetDiff() (string, error) { return "+++ b/a.go\n", nil }
func (f *fakeForge) PostComment(forges.ReviewComment) error {
	if f.failPost {
		return errors.New("постинг не удался")
	}
	return nil
}
func (f *fakeForge) PostSummary(string) error { return nil }
func (f *fakeForge) Approve(string) error     { f.approves++; return nil }

func TestApproveBlockedWhenPostFailed(t *testing.T) {
	ff := &fakeForge{failPost: true}
	cr := &Codereviewer{forge: ff}

	// Публикуем замечание — оно падает, ошибка фиксируется на структуре.
	reviewArgs := map[string]any{
		"comments": `[{"file_path":"a.go","line":1,"text":"x"}]`,
	}
	_ = cr.ReviewMr(reviewArgs)

	if len(cr.postErrors) == 0 {
		t.Fatal("ожидалась зафиксированная ошибка постинга")
	}

	// Ответ ApproveMr не должен вызвать реальный Approve.
	resp := cr.ApproveMr(map[string]any{})
	var out map[string]string
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] != "error" {
		t.Errorf("ApproveMr вернул status=%q, ожидали error", out["status"])
	}
	if ff.approves != 0 {
		t.Errorf("Approve вызван %d раз, ожидали 0 после сбоя постинга", ff.approves)
	}
}

func TestApproveProceedsWhenNoPostFailure(t *testing.T) {
	ff := &fakeForge{}
	cr := &Codereviewer{forge: ff}

	resp := cr.ApproveMr(map[string]any{})
	var out map[string]string
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatal(err)
	}
	if out["status"] == "error" {
		t.Errorf("неожиданная ошибка: %s", out["message"])
	}
	if ff.approves != 1 {
		t.Errorf("Approve вызван %d раз, ожидали 1", ff.approves)
	}
}

func TestApproveBlockedOnCritical(t *testing.T) {
	ff := &fakeForge{}
	cr := &Codereviewer{forge: ff, cfg: Config{BlockOnCritical: true}}

	// Критичное замечание. ApproveMr должен отказаться ставить апрув.
	_ = cr.ReviewMr(map[string]any{
		"comments": `[{"file_path":"a.go","line":1,"text":"критично: уязвимость SQL-инъекции"}]`,
	})

	resp := cr.ApproveMr(map[string]any{})
	var out map[string]string
	_ = json.Unmarshal(resp, &out)
	if out["status"] != "error" {
		t.Errorf("ожидали блокировку апрува на критичном замечании, got %q", out["status"])
	}
	if ff.approves != 0 {
		t.Errorf("Approve вызван %d раз, ожидали 0 при критичных замечаниях", ff.approves)
	}
}

func TestCommentLimitTruncates(t *testing.T) {
	ff := &fakeForge{}
	cr := &Codereviewer{forge: ff, cfg: Config{MaxComments: 2}}

	payload := `[
		{"file_path":"a.go","line":1,"text":"c1"},
		{"file_path":"a.go","line":2,"text":"c2"},
		{"file_path":"a.go","line":3,"text":"c3"}
	]`
	_ = cr.ReviewMr(map[string]any{"comments": payload})

	if cr.commentCount != 2 {
		t.Errorf("commentCount = %d, ожидали 2 (лимит)", cr.commentCount)
	}
}

func TestIsCritical(t *testing.T) {
	if !isCritical("критично: что-то сломано") {
		t.Error("ожидали критичность для 'критично:'")
	}
	if !isCritical("  КРИТИЧНО: сервер падает") {
		t.Error("ожидали критичность без учета регистра и пробелов")
	}
	if isCritical("для заметки: улучшение стиля") {
		t.Error("'для заметки:' не должно считаться критичным")
	}
}

func TestFilterGeneratedDiff(t *testing.T) {
	diff := `diff --git a/app.go b/app.go
+++ b/app.go
@@ -1,3 +1,4 @@
+fmt.Println("hi")
diff --git a/package-lock.json b/package-lock.json
+++ b/package-lock.json
@@ -1,5 +1,6 @@
+ new
diff --git a/bundle.min.js b/bundle.min.js
+++ b/bundle.min.js
@@ -1,1 +1,1 @@
minified
`

	filtered := filterGeneratedDiff(diff)

	if !strings.Contains(filtered, "fmt.Println") {
		t.Error("app.go ханк должен остаться в отфильтрованном диффе")
	}
	if strings.Contains(filtered, "new") || strings.Contains(filtered, "minified") {
		t.Error("package-lock.json и min.js не должны попасть в отфильтрованный дифф")
	}
}
