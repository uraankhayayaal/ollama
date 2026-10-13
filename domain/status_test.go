package domain

import "testing"

func TestValidateTransitionFSM(t *testing.T) {
	valid := []struct{ from, to Status }{
		{StatusNew, StatusAnalysis},
		{StatusNew, StatusCancelled},
		{StatusAnalysis, StatusReady},
		{StatusReady, StatusInProgress},
		{StatusInProgress, StatusDone},
		{StatusInProgress, StatusTesting},
		{StatusTesting, StatusInProgress},
		{StatusTesting, StatusDone},
		{StatusDone, StatusDone},
		{StatusNew, StatusHumanHelp},
		{StatusTesting, StatusHumanHelp},
		{StatusHumanHelp, StatusReady},
		{StatusHumanHelp, StatusInProgress},
		{StatusHumanHelp, StatusCancelled},
	}
	for _, c := range valid {
		if err := ValidateTransition(c.from, c.to); err != nil {
			t.Errorf("%s -> %s должно быть разрешено: %v", c.from, c.to, err)
		}
	}

	invalid := []struct{ from, to Status }{
		{StatusNew, StatusReady},
		{StatusNew, StatusDone},
		{StatusReady, StatusDone},
		{StatusInProgress, StatusAnalysis},
		{StatusDone, StatusNew},
		{StatusNew, "unknown"},
		{StatusDone, StatusHumanHelp},
		{StatusCancelled, StatusHumanHelp},
		{StatusHumanHelp, StatusDone},
		{StatusHumanHelp, StatusAnalysis},
		{StatusReady, StatusTesting},
	}
	for _, c := range invalid {
		if err := ValidateTransition(c.from, c.to); err == nil {
			t.Errorf("%s -> %s должно быть запрещено", c.from, c.to)
		}
	}
}

func TestStatusTerminalExcludesHumanHelp(t *testing.T) {
	if StatusHumanHelp.Terminal() {
		t.Fatal("human_help — боковая ветка, не терминален")
	}
	if !StatusDone.Terminal() || !StatusCancelled.Terminal() {
		t.Fatal("done/cancelled должны быть терминальными")
	}
}
