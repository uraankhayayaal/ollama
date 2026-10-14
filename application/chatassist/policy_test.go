// Тесты политики диалога чат-ассистента (application/chatassist). Вынесено из
// `server/chatassist.go`/`server/actions.go` (Этап 6, срез F3w).
package chatassist

import (
	"strings"
	"testing"

	"ai/chat"
)

func TestConfirmAffirmative(t *testing.T) {
	for _, s := range []string{"да", "Да!", "ок", "lf", "делай", "конечно", "yes, делай"} {
		if !ConfirmAffirmative(s) {
			t.Errorf("ConfirmAffirmative(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"нет", "не надо", "передумал", "какой эпик нужен?"} {
		if ConfirmAffirmative(s) {
			t.Errorf("ConfirmAffirmative(%q) = true, want false", s)
		}
	}
}

func TestBareConfirm(t *testing.T) {
	for _, s := range []string{"да", "давай", "lf", "ок, давай"} {
		if !BareConfirm(s) {
			t.Errorf("BareConfirm(%q) = false", s)
		}
	}
	// Согласие внутри длинной фразы — не «чистое» согласие.
	if BareConfirm("давай посмотрим, что там по безопасности проекта") {
		t.Fatal("длинная фраза не должна быть BareConfirm")
	}
	if BareConfirm("") {
		t.Fatal("пустая строка не согласие")
	}
}

func TestLooksLikeBoardWriteClaim(t *testing.T) {
	for _, s := range []string{"Завёл эпик по аутентификации", "создал баг: падает тест", "Запустил оркестрацию"} {
		if !LooksLikeBoardWriteClaim(s) {
			t.Errorf("LooksLikeBoardWriteClaim(%q) = false", s)
		}
	}
	for _, s := range []string{"эпиков на доске нет", "может создать эпик?", "расскажу про баги"} {
		if LooksLikeBoardWriteClaim(s) {
			t.Errorf("LooksLikeBoardWriteClaim(%q) = true", s)
		}
	}
}

func TestAsksSomething(t *testing.T) {
	for _, s := range []string{"Удалить Rust полностью?", "Подтвердите: удалить?", "Выберите вариант", "(да/нет)"} {
		if !AsksSomething(s) {
			t.Errorf("AsksSomething(%q) = false", s)
		}
	}
	for _, s := range []string{"", "готово", "сделал задачу"} {
		if AsksSomething(s) {
			t.Errorf("AsksSomething(%q) = true", s)
		}
	}
}

func TestPrevUserDiffers(t *testing.T) {
	if PrevUserDiffers("Какой эпик создать, про RAG или про билд?", "да") {
		t.Fatal("короткий ответ — не смена темы")
	}
	if !PrevUserDiffers("Какой эпик создать?", "вообще-то давай посмотрим, что там по безопасности проекта") {
		t.Fatal("длинная новая реплика — смена темы")
	}
	if PrevUserDiffers("", "что-то") {
		t.Fatal("пустой вопрос → false")
	}
}

func TestAskSummary(t *testing.T) {
	if got := AskSummary(nil); got != "" {
		t.Fatalf("nil → %q", got)
	}
	a := &chat.Ask{Questions: []chat.AskQuestion{{Text: "первый"}, {Text: "  "}, {Text: "второй"}}}
	if got := AskSummary(a); got != "первый; второй" {
		t.Fatalf("AskSummary = %q", got)
	}
}

func hist(msgs ...chat.Message) []chat.Message { return msgs }

func TestActionAnchorConfirmLoop(t *testing.T) {
	h := hist(
		chat.Message{Role: chat.RoleUser, Content: "давай удалим rust"},
		chat.Message{Role: chat.RoleAssistant, Content: "Удалить Rust полностью? (да/нет)"},
		chat.Message{Role: chat.RoleUser, Content: "да"},
	)
	got := ActionAnchor(h, "да")
	if !strings.Contains(got, "ВЫПОЛНЯЙ") {
		t.Fatalf("после подтверждения якорь должен требовать действия:\n%s", got)
	}
}

func TestActionAnchorShortAffirmative(t *testing.T) {
	h := hist(
		chat.Message{Role: chat.RoleUser, Content: "давай удалим rust"},
		chat.Message{Role: chat.RoleAssistant, Content: "Удалить Rust полностью? (да/нет)"},
		chat.Message{Role: chat.RoleUser, Content: "lf"},
	)
	if got := ActionAnchor(h, "lf"); !strings.Contains(got, "ВЫПОЛНЯЙ") {
		t.Fatalf("«lf» не распознано как согласие: %q", got)
	}
}

func TestActionAnchorNewTopic(t *testing.T) {
	h := hist(
		chat.Message{Role: chat.RoleUser, Content: "а"},
		chat.Message{Role: chat.RoleAssistant, Content: "Какой эпик создать, про RAG или про билд?"},
		chat.Message{Role: chat.RoleAssistant, Content: "Уточните, о каком модуле речь?"},
		chat.Message{Role: chat.RoleUser, Content: "вообще-то давай посмотрим, что там по безопасности проекта"},
	)
	if got := ActionAnchor(h, "вообще-то давай посмотрим, что там по безопасности проекта"); got != "" {
		t.Fatalf("на новой теме якорь должен быть пуст:\n%s", got)
	}
}

func TestActionAnchorTwoQuestionsNonYes(t *testing.T) {
	h := hist(
		chat.Message{Role: chat.RoleAssistant, Content: "Первый вопрос?"},
		chat.Message{Role: chat.RoleAssistant, Content: "Второй вопрос?"},
		chat.Message{Role: chat.RoleUser, Content: "хм"},
	)
	if got := ActionAnchor(h, "хм"); !strings.Contains(got, "ВНИМАНИЕ") {
		t.Fatalf("два вопроса подряд без явного ответа → лимитер:\n%s", got)
	}
}

func TestActionAnchorEmpty(t *testing.T) {
	if got := ActionAnchor(nil, "привет"); got != "" {
		t.Fatalf("пустая история → пусто, got %q", got)
	}
}

func TestActionAnchorAskRole(t *testing.T) {
	// Структурированный вопрос (role=ask) распознаётся как вопрос ассистента.
	h := hist(
		chat.Message{Role: chat.RoleAsk, Ask: &chat.Ask{Questions: []chat.AskQuestion{{Text: "Удалить?"}}}},
		chat.Message{Role: chat.RoleUser, Content: "да"},
	)
	if got := ActionAnchor(h, "да"); !strings.Contains(got, "ВЫПОЛНЯЙ") {
		t.Fatalf("ask-вопрос не распознан: %q", got)
	}
}
