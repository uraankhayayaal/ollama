package factory

import (
	"ai/domain"
	"reflect"
	"testing"
)

// TestRoleFactoryMapsRoles фиксирует соответствие «роль → конкретный агент»
// в порту planner.AgentFactory: инверсия зависимости не должна менять
// поведение выбора специалиста/лида по маркерам assigned_role.
func TestRoleFactoryMapsRoles(t *testing.T) {
	f := roleFactory{}
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

func TestRoleFactoryStep(t *testing.T) {
	f := roleFactory{}
	cases := map[domain.AgentType]string{
		domain.AgentBackendDev:   "*developer.BackendDeveloper",
		domain.AgentFrontendDev:  "*developer.FrontendDeveloper",
		domain.AgentDevops:       "*devops.Devops",
		domain.AgentDevopsLead:   "*devopslead.DevopsLead",
		domain.AgentQAEngineer:   "*qaengineer.QAEngineer",
		domain.AgentFrontendLead: "*frontendlead.FrontendLead",
		domain.AgentBackendLead:  "*backendlead.BackendLead",
	}
	for at, want := range cases {
		got := reflect.TypeOf(f.Step(at, "proj", "p")).String()
		if got != want {
			t.Errorf("Step(%s) = %s, ожидался %s", at, got, want)
		}
	}
	if a := f.Step(domain.AgentType("unknown"), "p", "p"); a != nil {
		t.Errorf("Step(unknown) = %v, ожидался nil", a)
	}
}
