package scope

import (
	"reflect"
	"testing"
)

func checkConfig() Config {
	c := testConfig()
	c.Components = []string{"system/*/", "apps/*.yaml"}
	return c
}

func TestCheckClean(t *testing.T) {
	tracked := []string{"system/loki/values.yaml", "system/kured/values.yaml", "docs/a.md", "README.md", "Makefile"}
	if problems := checkConfig().Check(tracked, testSpecs()); len(problems) != 0 {
		t.Errorf("problems: %q", problems)
	}
}

func TestCheckUncoveredComponent(t *testing.T) {
	tracked := []string{"system/loki/values.yaml", "system/newthing/values.yaml", "system/newthing/Chart.yaml", "apps/new.yaml"}
	got := checkConfig().Check(tracked, testSpecs())
	want := []string{
		"apps/new.yaml: in no group of test/scopes.yaml (add it to a group, or to skip)",
		"system/newthing: in no group of test/scopes.yaml (add it to a group, or to skip)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

// A label typed wrong, in a filter or on a spec, is outside labels:
func TestCheckUnknownLabel(t *testing.T) {
	c := checkConfig()
	c.Groups = append(c.Groups, Group{Paths: []string{"system/grafana/**"}, Tests: "logs || grafana"})
	specs := append(testSpecs(), Spec{Name: "TrivyOperator", Labels: []string{"sandbox", "trivy-operater"}, File: "test/e2e/trivy_operator_test.go"})
	got := c.Check(nil, specs)
	want := []string{
		`TrivyOperator (test/e2e/trivy_operator_test.go): label "trivy-operater" is not in labels:`,
		`group 6: "grafana" is not in labels:`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestCheckUnusedLabel(t *testing.T) {
	c := checkConfig()
	c.Labels = append(c.Labels, "grafana")
	got := c.Check(nil, testSpecs())
	want := []string{`labels: "grafana" labels no spec`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

// A filter of known labels that selects no spec (a spec renamed or relabelled)
func TestCheckStaleFilter(t *testing.T) {
	c := checkConfig()
	c.Groups = append(c.Groups, Group{Paths: []string{"system/grafana/**"}, Tests: "logs && identity"})
	got := c.Check(nil, testSpecs())
	want := []string{`group 6: "logs && identity" selects no spec`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

// An alone label must name specs labelled alone, or they would run in the main run (and the alone run select nothing)
func TestCheckAloneWithoutAloneSpec(t *testing.T) {
	c := checkConfig()
	c.Alone = append(c.Alone, "logs")
	got := c.Check(nil, testSpecs())
	want := []string{`alone: "logs" labels no spec labelled alone`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

// A spec labelled alone that alone: does not list would never run: the main run leaves it out
func TestCheckAloneSpecNotListed(t *testing.T) {
	c := checkConfig()
	c.Alone = []string{"node-restart"}
	got := c.Check(nil, testSpecs())
	want := []string{`BackupRestore (): labelled alone, but alone: lists none of its labels`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}
