package domain

import "testing"

func TestValidateTransitionEpic(t *testing.T) {
	valid := []struct {
		from, to Status
	}{
		{StatusDraft, StatusBusinessReview},
		{StatusDraft, StatusCancelled},
		{StatusBusinessReview, StatusSystemAnalysis},
		{StatusBusinessReview, StatusDraft},
		{StatusBusinessReview, StatusCancelled},
		{StatusSystemAnalysis, StatusDone},
		{StatusSystemAnalysis, StatusCancelled},
		{StatusDone, StatusDone},
		{StatusCancelled, StatusCancelled},
	}
	for _, c := range valid {
		if err := ValidateTransition(c.from, c.to, EntityTypeEpic); err != nil {
			t.Errorf("%s -> %s должно быть разрешено: %v", c.from, c.to, err)
		}
	}

	invalid := []struct {
		from, to Status
	}{
		{StatusDraft, StatusDone},
		{StatusDraft, StatusSystemAnalysis},
		{StatusBusinessReview, StatusDone},
		{StatusSystemAnalysis, StatusBusinessReview},
		{StatusDone, StatusCancelled},
		{StatusDone, StatusDraft},
		{StatusCancelled, StatusDraft},
		{StatusDraft, "unknown"},
	}
	for _, c := range invalid {
		if err := ValidateTransition(c.from, c.to, EntityTypeEpic); err == nil {
			t.Errorf("%s -> %s должно быть запрещено", c.from, c.to)
		}
	}
}

func TestValidateTransitionTask(t *testing.T) {
	valid := []struct {
		from, to Status
	}{
		{StatusInfra, StatusTestCases},
		{StatusInfra, StatusCancelled},
		{StatusTestCases, StatusDevelopment},
		{StatusTestCases, StatusCancelled},
		{StatusDevelopment, StatusDone},
		{StatusDevelopment, StatusCancelled},
		{StatusDone, StatusDone},
		{StatusCancelled, StatusCancelled},
	}
	for _, c := range valid {
		if err := ValidateTransition(c.from, c.to, EntityTypeTask); err != nil {
			t.Errorf("%s -> %s должно быть разрешено: %v", c.from, c.to, err)
		}
	}

	invalid := []struct {
		from, to Status
	}{
		{StatusInfra, StatusDone},
		{StatusInfra, StatusDevelopment},
		{StatusTestCases, StatusDone},
		{StatusDevelopment, StatusTestCases},
		{StatusDevelopment, StatusInfra},
		{StatusTestCases, StatusInfra},
		{StatusDone, StatusCancelled},
		{StatusDone, StatusInfra},
		{StatusCancelled, StatusInfra},
		{StatusInfra, "unknown"},
	}
	for _, c := range invalid {
		if err := ValidateTransition(c.from, c.to, EntityTypeTask); err == nil {
			t.Errorf("%s -> %s должно быть запрещено", c.from, c.to)
		}
	}
}

func TestValidateTransitionWrongEntityType(t *testing.T) {
	// Epic-переход должен быть запрещён для Task и наоборот
	if err := ValidateTransition(StatusDraft, StatusBusinessReview, EntityTypeTask); err == nil {
		t.Error("epic-переход должен быть запрещён для task")
	}
	if err := ValidateTransition(StatusInfra, StatusTestCases, EntityTypeEpic); err == nil {
		t.Error("task-переход должен быть запрещён для epic")
	}
}

func TestStatusTerminal(t *testing.T) {
	if !StatusDone.Terminal() {
		t.Fatal("done должен быть терминальным")
	}
	if !StatusCancelled.Terminal() {
		t.Fatal("cancelled должен быть терминальным")
	}
	for _, s := range []Status{
		StatusDraft, StatusBusinessReview, StatusSystemAnalysis,
		StatusInfra, StatusTestCases, StatusDevelopment,
	} {
		if s.Terminal() {
			t.Fatalf("%s не должен быть терминальным", s)
		}
	}
}

func TestStatusValid(t *testing.T) {
	for _, s := range []Status{
		StatusDraft, StatusBusinessReview, StatusSystemAnalysis,
		StatusInfra, StatusTestCases, StatusDevelopment,
		StatusDone, StatusCancelled,
	} {
		if !s.Valid() {
			t.Fatalf("%s должен быть валидным", s)
		}
	}
	if Status("garbage").Valid() {
		t.Fatal("неизвестный статус должен быть невалидным")
	}
}

func TestStatusErrorFields(t *testing.T) {
	err := ValidateTransition(StatusDraft, StatusDone, EntityTypeEpic)
	if err == nil {
		t.Fatal("ожидаемая ошибка")
	}
	se, ok := err.(*StatusError)
	if !ok {
		t.Fatal("ожидаем *StatusError")
	}
	if se.EntityType != EntityTypeEpic {
		t.Fatalf("EntityType = %s, want %s", se.EntityType, EntityTypeEpic)
	}
	if se.From != StatusDraft || se.To != StatusDone {
		t.Fatalf("From = %s, To = %s", se.From, se.To)
	}
}
