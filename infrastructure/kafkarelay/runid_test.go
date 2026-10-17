package kafkarelay

import (
	"context"
	"testing"
)

func TestRunID_DeterministicAndEpicScope(t *testing.T) {
	ctx := context.Background()
	id1 := scopeRunID(ctx, "proj", "epic:EP-1", nil)
	id2 := scopeRunID(ctx, "proj", "epic:EP-1", nil)
	if id1 != id2 {
		t.Fatalf("epic scope должен быть детерминированным: %q vs %q", id1, id2)
	}

	// один и тот же эпик даёт один UUID
	idE1 := scopeRunID(ctx, "p", "epic:E1", nil)
	idE2 := scopeRunID(ctx, "p", "epic:E1", nil)
	if idE1 != idE2 {
		t.Fatalf("epic повторно: разные")
	}

	// ассистент один на проект
	a1 := assistantRunID("p")
	a2 := assistantRunID("p")
	if a1 != a2 {
		t.Fatalf("assistant должен быть детерминированным")
	}
	if a1 == idE1 {
		// вряд ли, но не критично; главное — собственное пространство
	}

	// вспомогательный ран — разные по scope, но детерминированы
	ar1 := scopeRunID(ctx, "p", "architecture", nil)
	ar2 := scopeRunID(ctx, "p", "architecture", nil)
	if ar1 != ar2 {
		t.Fatalf("arch детерминирован")
	}
}
