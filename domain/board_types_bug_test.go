package domain

import "testing"

func TestBugStatusBlocked(t *testing.T) {
	for _, tc := range []struct {
		st   BugStatus
		want bool
	}{
		{BugStatusNew, false},
		{BugStatusConfirmed, false},
		{BugStatusFix, false},
		{BugStatusFeature, false},
		{BugStatusWontFix, false},
		{BugStatusFixed, false},
		{BugStatusSlop, true},
	} {
		if got := tc.st.Blocked(); got != tc.want {
			t.Errorf("BugStatus(%s).Blocked() = %v, want %v", tc.st, got, tc.want)
		}
	}
}

func TestValidateBugTransitionManualFromBlocked(t *testing.T) {
	for _, to := range []BugStatus{
		BugStatusNew, BugStatusConfirmed, BugStatusFix, BugStatusFeature,
		BugStatusWontFix, BugStatusFixed,
	} {
		if err := ValidateBugTransitionManual(BugStatusSlop, to); err == nil {
			t.Errorf("из заблокированного slop в %s должен быть запрещён переход", to)
		}
	}
	// Тот же статус — идемпотентно (no-op, не ошибка).
	if err := ValidateBugTransitionManual(BugStatusSlop, BugStatusSlop); err != nil {
		t.Errorf("тот же статус не должен быть ошибкой: %v", err)
	}
}

func TestValidateBugTransitionManualAllowsAnyFromUnblocked(t *testing.T) {
	for _, from := range []BugStatus{BugStatusNew, BugStatusConfirmed, BugStatusFix} {
		for _, to := range []BugStatus{
			BugStatusNew, BugStatusConfirmed, BugStatusSlop, BugStatusFix,
			BugStatusFeature, BugStatusWontFix, BugStatusFixed,
		} {
			if err := ValidateBugTransitionManual(from, to); err != nil {
				t.Errorf("ручной переход %s -> %s не должен быть запрещён: %v", from, to, err)
			}
		}
	}
}

func TestValidateBugTransitionManualUnknown(t *testing.T) {
	if err := ValidateBugTransitionManual(BugStatusNew, BugStatus("garbage")); err == nil {
		t.Fatal("неизвестный статус назначения должен быть запрещён")
	}
}

func TestSortBugReports(t *testing.T) {
	bugs := []*BugReport{
		{BugID: "B-3", Priority: 0},
		{BugID: "B-1", Priority: 5},
		{BugID: "B-4", Status: BugStatusSlop, Priority: 9}, // заблокированный — вниз
		{BugID: "B-2", Priority: 5},
	}
	SortBugReports(bugs)
	want := []string{"B-1", "B-2", "B-3", "B-4"}
	for i, b := range bugs {
		if b.BugID != want[i] {
			t.Fatalf("порядок = %v, want %v", ids(bugs), want)
		}
	}
}

func ids(bugs []*BugReport) []string {
	out := make([]string, len(bugs))
	for i, b := range bugs {
		out[i] = b.BugID
	}
	return out
}
