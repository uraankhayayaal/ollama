package planner

import (
	"reflect"
	"testing"
)

// TestRoleAgentFactoryMapsRoles фиксирует соответствие «роль → конкретный агент»
// в порту AgentFactory: инверсия зависимости не должна менять поведение выбора
// специалиста/лида по маркерам assigned_role.
func TestRoleAgentFactoryMapsRoles(t *testing.T) {
	f := roleAgentFactory{}
	typeName := func(a any) string {
		if a == nil {
			return "<nil>"
		}
		return reflect.TypeOf(a).String()
	}

	specialistCases := []struct {
		role string
		want string
	}{
		{"QA Engineer", "*qaengineer.QAEngineer"},
		{"тестировщик", "*qaengineer.QAEngineer"},
		{"DevOps Engineer", "*devops.Devops"},
		{"инфраструктурный инженер", "*devops.Devops"},
		{"React Developer", "*developer.FrontendDeveloper"},
		{"Frontend Engineer", "*developer.FrontendDeveloper"},
		{"Senior Go Developer", "*developer.BackendDeveloper"},
		{"Backend Lead", "*developer.BackendDeveloper"},
	}
	for _, c := range specialistCases {
		if got := typeName(f.Specialist("p", c.role, "промпт")); got != c.want {
			t.Errorf("Specialist(%q) = %s, ожидался %s", c.role, got, c.want)
		}
	}

	leadCases := []struct {
		role string
		want string
	}{
		{"QA Lead", "*qaengineer.QAEngineer"},
		{"DevOps Lead", "*devopslead.DevopsLead"},
		{"Frontend Lead", "*frontendlead.FrontendLead"},
		{"Backend Lead", "*backendlead.BackendLead"},
	}
	for _, c := range leadCases {
		if got := typeName(f.Lead("p", c.role, "промпт")); got != c.want {
			t.Errorf("Lead(%q) = %s, ожидался %s", c.role, got, c.want)
		}
	}
}

func TestRoleAgentFactoryStep(t *testing.T) {
	f := roleAgentFactory{}
	cases := map[AgentType]string{
		AgentBackendDev:   "*developer.BackendDeveloper",
		AgentFrontendDev:  "*developer.FrontendDeveloper",
		AgentDevops:       "*devops.Devops",
		AgentDevopsLead:   "*devopslead.DevopsLead",
		AgentQAEngineer:   "*qaengineer.QAEngineer",
		AgentFrontendLead: "*frontendlead.FrontendLead",
		AgentBackendLead:  "*backendlead.BackendLead",
	}
	for at, want := range cases {
		got := reflect.TypeOf(f.Step(&Step{Agent: at, Prompt: "p"}, "proj")).String()
		if got != want {
			t.Errorf("Step(%s) = %s, ожидался %s", at, got, want)
		}
	}
	if a := f.Step(&Step{Agent: AgentType("unknown")}, "p"); a != nil {
		t.Errorf("Step(unknown) = %v, ожидался nil", a)
	}
}
