package planner

import (
	"ai/agents/factory"
	"ai/tools"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	SetPlannerToolBuilder(tools.PlannerToolSet)
	SetLSPDiagnoser(tools.LSPDiagnoserAdapter{})
	SetSnapshotFactory(tools.SnapshotAdapterFactory{})
	SetPublicAPIAuditor(tools.PublicAPIAuditorAdapter{})
	SetAgentFactory(factory.New())
	os.Exit(m.Run())
}
