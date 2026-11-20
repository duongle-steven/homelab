package scope

import (
	"reflect"
	"strings"
	"testing"
)

// deployConfig is testConfig with groups: metrics (monitoring), observability (logs, needs metrics), backup
// (backup-restore, needs git), git, and an app ntfy; the core serves smoke and identity
func deployConfig() Config {
	c := testConfig()
	c.Labels = append(c.Labels, "monitoring")
	c.Core = Core{Components: []string{"system/istio-system", "platform/kanidm"}, Labels: []string{"smoke", "identity", "node-restart", "proof", "security-logs"}}
	c.DeployGroups = map[string]DeployGroup{
		"metrics":       {Components: []string{"system/monitoring-system"}, Labels: []string{"monitoring"}},
		"observability": {Components: []string{"system/loki"}, Labels: []string{"logs"}, Needs: []string{"metrics"}},
		"backup":        {Components: []string{"system/velero"}, Labels: []string{"backup-restore"}, Needs: []string{"git"}},
		"git":           {Components: []string{"platform/forgejo"}},
		"ntfy":          {Components: []string{"apps/ntfy.yaml"}},
	}
	return c
}

func TestGroupsFromLabels(t *testing.T) {
	got := deployConfig().GroupsFor("logs", testSpecs(), nil)
	if want := []string{"metrics", "observability"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupsFor(logs) = %v, want %v", got, want)
	}
}

// A selected spec's other labels count too: SecurityLogsK3sExec carries logs besides security-logs
func TestGroupsFromSelectedSpecsLabels(t *testing.T) {
	got := deployConfig().GroupsFor("security-logs", testSpecs(), nil)
	if want := []string{"metrics", "observability"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupsFor(security-logs) = %v, want %v", got, want)
	}
}

func TestGroupsFromChanges(t *testing.T) {
	got := deployConfig().GroupsFor("", testSpecs(), []string{"apps/ntfy.yaml", "docs/x.md"})
	if want := []string{"ntfy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupsFor(changed ntfy) = %v, want %v", got, want)
	}
}

func TestGroupsNeeds(t *testing.T) {
	got := deployConfig().GroupsFor("backup-restore", testSpecs(), nil)
	if want := []string{"backup", "git"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupsFor(backup-restore) = %v, want %v", got, want)
	}
}

func TestAllGroups(t *testing.T) {
	if got := deployConfig().AllGroups(); !reflect.DeepEqual(got, []string{"backup", "git", "metrics", "ntfy", "observability"}) {
		t.Fatalf("AllGroups = %v", got)
	}
}

func TestCheckGroups(t *testing.T) {
	c := deployConfig()
	c.DeployGroups["twice"] = DeployGroup{Components: []string{"system/loki"}}
	c.DeployGroups["broken"] = DeployGroup{Needs: []string{"nowhere"}, Labels: []string{"no-such-label"}}
	problems := strings.Join(c.CheckGroups(testSpecs()), "\n")
	for _, want := range []string{"system/loki: in groups observability and twice", "broken: needs nowhere, which is no group",
		"broken: label no-such-label is not in labels"} {
		if !strings.Contains(problems, want) {
			t.Errorf("CheckGroups misses %q:\n%s", want, problems)
		}
	}
	// A label neither the core nor any group serves
	c = deployConfig()
	c.Core.Labels = []string{"smoke"}
	if problems := strings.Join(c.CheckGroups(testSpecs()), "\n"); !strings.Contains(problems, "identity: served by no group nor the core") {
		t.Errorf("CheckGroups misses an unserved label:\n%s", problems)
	}
	if problems := deployConfig().CheckGroups(testSpecs()); len(problems) > 0 {
		t.Errorf("clean map: %v", problems)
	}
}
