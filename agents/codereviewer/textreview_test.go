package codereviewer

import (
	"encoding/json"
	"testing"
)

// Пример реального ответа модели (YandexGPT), который уходит текстом вместо
// вызова инструментов.
const sampleTextReview = `ReviewMr для ` + "`core/components/minishop3/src/Services/Customer/AuthManager.php`" + `:

**Файл:** ` + "`core/components/minishop3/src/Services/Customer/AuthManager.php`" + `

1.  **Строка:** ~285 (в блоке ` + "`prefillOrderDraftFromCustomer`" + `)
    **Текст:** ` + "`критично:`" + ` Проверка наличия сервиса через ` + "`$this->modx->services->has('ms3_order_address_manager')`" + ` делает метод уязвимым.

2.  **Строка:** ~288
    **Текст:** ` + "`для заметки:`" + ` Использование ` + "`CartDraftContext::resolve`" + ` здесь важно.


**Файл:** ` + "`core/components/minishop3/src/Services/Order/OrderAddressManager.php`" + `

1.  **Строка:** ~198
    **Текст:** ` + "`критично:`" + ` Перезапись переменной ` + "`$orderData`" + ` затрёт локальную переменную.
`

func TestParseTextReview(t *testing.T) {
	comments := parseTextReview(sampleTextReview)
	if len(comments) != 3 {
		t.Fatalf("ожидали 3 замечания, got %d: %#v", len(comments), comments)
	}

	want := []struct {
		file string
		line int
	}{
		{"core/components/minishop3/src/Services/Customer/AuthManager.php", 285},
		{"core/components/minishop3/src/Services/Customer/AuthManager.php", 288},
		{"core/components/minishop3/src/Services/Order/OrderAddressManager.php", 198},
	}
	for i, w := range want {
		if comments[i].FilePath != w.file {
			t.Errorf("замечание %d: file = %q, ожидали %q", i, comments[i].FilePath, w.file)
		}
		if comments[i].Line != w.line {
			t.Errorf("замечание %d: line = %d, ожидали %d", i, comments[i].Line, w.line)
		}
		if comments[i].Text == "" {
			t.Errorf("замечание %d: пустой текст", i)
		}
	}
}

func TestPublishParsedReviewPostsAndCounts(t *testing.T) {
	ff := &fakeForge{}
	cr := &Codereviewer{
		forge: ff,
		diff: `+++ b/core/components/minishop3/src/Services/Customer/AuthManager.php
@@ -284,2 +284,2 @@
+285 line here
+286 another`,
	}

	n := cr.PublishParsedReview(sampleTextReview)
	// Валидным должен пройти только реально существующий файл/строка.
	// В этом диффе есть только AuthManager.php, но не OrderAddressManager.php.
	if n < 1 {
		t.Errorf("ожидали >= 1 опубликованное замечание, got %d", n)
	}
	if cr.commentCount != n {
		t.Errorf("commentCount = %d, не совпадает с вернувшимся %d", cr.commentCount, n)
	}
}

func TestPublishParsedReviewEmptyContent(t *testing.T) {
	ff := &fakeForge{}
	cr := &Codereviewer{forge: ff}
	if n := cr.PublishParsedReview(""); n != 0 {
		t.Errorf("пустой контент: ожидали 0, got %d", n)
	}
}

func TestPublishParsedReviewRejectsLiesWhenOutsideDiff(t *testing.T) {
	ff := &fakeForge{}
	// Дифф вообще не содержит tsp-файл, который модель выдумала.
	cr := &Codereviewer{
		forge: ff,
		diff:  "+++ b/real.php\n",
	}

	// Парсер сам по себе отдаёт замечания, но фильтр по diff их отсекает.
	parsed := parseTextReview(sampleTextReview)
	if len(parsed) == 0 {
		t.Fatal("парсер должен вернуть замечания до фильтрации")
	}
	args := map[string]any{"comments": mustJSON(t, parsed)}
	_ = cr.ReviewMr(args)
	if cr.commentCount != 0 {
		t.Errorf("все замечания должны отсечься фильтром по diff, got %d", cr.commentCount)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
