package codereviewer

import (
	"encoding/json"
	"errors"
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
func (f *fakeForge) Approve() error { f.approves++; return nil }

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
