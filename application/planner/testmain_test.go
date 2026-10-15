package planner

import (
	"ai/tools"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	SetPlannerToolBuilder(tools.PlannerToolSet)
	SetLSPDiagnoser(tools.LSPDiagnoserAdapter{})
	SetSnapshotFactory(tools.SnapshotAdapterFactory{})
	SetPublicAPIAuditor(tools.PublicAPIAuditorAdapter{})
	os.Exit(m.Run())
}
