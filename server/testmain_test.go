package server

import (
	"ai/agents/factory"
	"ai/application/planner"
	"ai/tools"
	"os"
	"testing"
)

// TestMain повторяет подключение outer-зависимостей planner из main.go:
// серверные тесты создают gitflow-резолвер, который зовёт
// planner.SpecialistForRole → planner.AgentFactory. Без установленной фабрики
// порт по умолчанию возвращает nil-агентов.
func TestMain(m *testing.M) {
	planner.SetPlannerToolBuilder(tools.PlannerToolSet)
	planner.SetLSPDiagnoser(tools.LSPDiagnoserAdapter{})
	planner.SetSnapshotFactory(tools.SnapshotAdapterFactory{})
	planner.SetPublicAPIAuditor(tools.PublicAPIAuditorAdapter{})
	planner.SetAgentFactory(factory.New())
	os.Exit(m.Run())
}
