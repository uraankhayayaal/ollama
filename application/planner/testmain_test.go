package planner

import (
	"ai/tools"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	SetPlannerToolBuilder(tools.PlannerToolSet)
	SetOllamaToolConverter(tools.ToOllama)
	SetLSPDiagnoser(tools.LSPDiagnoserAdapter{})
	SetSnapshotFactory(tools.SnapshotAdapterFactory{})
	SetPublicAPIAuditor(tools.PublicAPIAuditorAdapter{})
	os.Exit(m.Run())
}
