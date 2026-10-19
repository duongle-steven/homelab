package scope

import (
	"reflect"
	"testing"
)

var checkTests = []string{"TestIdentityLogin", "TestLogsPodLines", "TestNodeRestart", "TestNodeRestartBlockers", "TestSmoke"}

func checkConfig() Config {
	c := testConfig()
	c.Components = []string{"system/*/", "apps/*.yaml"}
	return c
}

func TestCheckClean(t *testing.T) {
	tracked := []string{"system/loki/values.yaml", "system/kured/values.yaml", "docs/a.md", "README.md", "Makefile"}
	if problems := checkConfig().Check(tracked, checkTests); len(problems) != 0 {
		t.Errorf("problems: %q", problems)
	}
}

func TestCheckUncoveredComponent(t *testing.T) {
	tracked := []string{"system/loki/values.yaml", "system/newthing/values.yaml", "system/newthing/Chart.yaml", "apps/new.yaml"}
	got := checkConfig().Check(tracked, checkTests)
	want := []string{
		"apps/new.yaml: in no group of test/scopes.yaml (add it to a group, or to skip)",
		"system/newthing: in no group of test/scopes.yaml (add it to a group, or to skip)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestCheckStaleRegex(t *testing.T) {
	c := checkConfig()
	c.Groups = append(c.Groups, Group{Paths: []string{"system/grafana/**"}, Tests: "SSOGrafana"})
	got := c.Check(nil, checkTests)
	want := []string{`group 5: "SSOGrafana" matches no test`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestCheckMainTermPicksAloneTest(t *testing.T) {
	c := checkConfig()
	c.Groups = append(c.Groups, Group{Paths: []string{"metal/**"}, Tests: "Restart"})
	got := c.Check(nil, checkTests)
	want := []string{`group 5: "Restart" would run alone test TestNodeRestart with the others`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}
