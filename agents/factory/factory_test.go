package factory

import (
	"reflect"
	"testing"
)

// TestRoleFactoryMapsRoles фиксирует соответствие «роль → конкретный агент»
// в фабрике: инверсия зависимости не должна менять поведение выбора
// специалиста по маркерам assigned_role.
func TestRoleFactoryMapsRoles(t *testing.T) {
	f := roleFactory{}
	typeName := func(a any) string {
		if a == nil {
			return "<nil>"
		}
		t := reflect.TypeOf(a)
		for t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		return t.String()
	}

	specialistCases := []struct {
		role string
		want string
	}{
		{"React Developer", "*developer.FrontendDeveloper"},
		{"Frontend Engineer", "*developer.FrontendDeveloper"},
		{"Senior Go Developer", "*developer.BackendDeveloper"},
		{"Backend Lead", "*developer.BackendDeveloper"},
	}
	for _, c := range specialistCases {
		agent := f.Specialist("p", c.role, "промпт")
		if typeName(agent) != c.want {
			t.Errorf("Specialist(%q) = %s, ожидался %s", c.role, typeName(agent), c.want)
		}
	}
}

func TestRoleFactoryStep(t *testing.T) {
	f := roleFactory{}
	typeName := func(a any) string {
		if a == nil {
			return "<nil>"
		}
		t := reflect.TypeOf(a)
		for t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		return t.String()
	}

	cases := map[string]string{
		"backend": "*developer.BackendDeveloper",
		"frontend": "*developer.FrontendDeveloper",
	}
	for at, want := range cases {
		got := typeName(f.Step(at, "proj", "p"))
		if got != want {
			t.Errorf("Step(%s) = %s, ожидался %s", at, got, want)
		}
	}
}
