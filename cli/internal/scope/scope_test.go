package scope

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func testConfig() Config {
	return Config{
		Skip:   []string{"docs/**", "**/*.md"},
		Labels: []string{"sandbox", "testbed", "production", "alone", "smoke", "identity", "logs", "security-logs", "node-restart", "backup-restore", "proof"},
		Always: "smoke",
		Alone:  []string{"backup-restore", "node-restart"},
		Full:   "smoke || identity || logs || node-restart || backup-restore",
		Groups: []Group{
			{Paths: []string{"Makefile"}, Tests: FullKeyword},
			{Paths: []string{"system/loki/**"}, Tests: "logs"},
			{Paths: []string{"system/istio-system/**"}, Tests: "identity || node-restart"},
			{Paths: []string{"system/velero/**"}, Tests: "backup-restore"},
			{Paths: []string{"system/kured/**"}, Tests: ""},
		},
	}
}

// testSpecs are the specs testConfig's filters read: one per family, the testbed's proof with no sandbox label
func testSpecs() []Spec {
	return []Spec{
		{Name: "Smoke", Labels: []string{"sandbox", "testbed", "production", "smoke"}},
		{Name: "IdentityLogin", Labels: []string{"sandbox", "testbed", "identity"}},
		{Name: "LogsPodLines", Labels: []string{"sandbox", "testbed", "logs"}},
		{Name: "SecurityLogsK3sExec", Labels: []string{"sandbox", "testbed", "security-logs", "logs"}},
		{Name: "NodeRestart", Labels: []string{"sandbox", "node-restart", "alone"}},
		{Name: "BackupRestore", Labels: []string{"sandbox", "testbed", "backup-restore", "alone"}},
		{Name: "TestbedCeph", Labels: []string{"testbed", "proof"}},
	}
}

func noFilters(string) ([]string, error) { return nil, nil }

func selectFilter(t *testing.T, c Config, files []string, filters FileFilters) (string, []string) {
	t.Helper()
	s, err := c.Select(files, filters)
	if err != nil {
		t.Fatal(err)
	}
	return c.Filter(s, testSpecs())
}

const (
	fullMain = "(backup-restore || identity || logs || node-restart || smoke) && !alone"
)

func TestFilterOrsGroups(t *testing.T) {
	c := testConfig()
	all := []string{"backup-restore", "node-restart"}
	for _, tc := range []struct {
		name  string
		files []string
		main  string
		alone []string
	}{
		{"only skipped", []string{"docs/a.md", "README.md"}, "", nil},
		{"one group", []string{"system/loki/values.yaml"}, "(logs || smoke) && !alone", nil},
		{"two groups", []string{"system/loki/values.yaml", "system/istio-system/values.yaml"}, "(identity || logs || node-restart || smoke) && !alone", []string{"node-restart"}},
		{"group without tests", []string{"system/kured/values.yaml"}, "smoke && !alone", nil},
		{"unknown file", []string{"scripts/new"}, fullMain, all},
		{"full group", []string{"Makefile", "docs/a.md"}, fullMain, all},
		{"no changes", nil, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main, alone := selectFilter(t, c, tc.files, noFilters)
			if main != tc.main || !reflect.DeepEqual(alone, tc.alone) {
				t.Errorf("got main %q alone %q, want %q %q", main, alone, tc.main, tc.alone)
			}
		})
	}
}

// A group whose specs all run alone leaves the main run empty: --fail-on-empty would fail a filter that selects nothing
func TestFilterEmptyMainWhenOnlyAlone(t *testing.T) {
	c := testConfig()
	c.Always = ""
	main, alone := selectFilter(t, c, []string{"system/velero/values.yaml"}, noFilters)
	if main != "" || !reflect.DeepEqual(alone, []string{"backup-restore"}) {
		t.Errorf("got %q %q", main, alone)
	}
}

// A main filter that matches only the testbed's specs prints nothing: the sandbox has none of them
func TestFilterEmptyMainForTestbedOnly(t *testing.T) {
	main, alone := testConfig().Filter(ManualSelection("proof"), testSpecs())
	if main != "" || alone != nil {
		t.Errorf("got %q %q", main, alone)
	}
}

// An alone spec reached through a manual filter runs alone, never in the main run
func TestFilterManualReachesAlone(t *testing.T) {
	main, alone := testConfig().Filter(ManualSelection("node-restart || logs"), testSpecs())
	if main != "(logs || node-restart) && !alone" || !reflect.DeepEqual(alone, []string{"node-restart"}) {
		t.Errorf("got %q %q", main, alone)
	}
}

// Alone specs run in alone:'s order, whatever the filter's
func TestFilterAloneOrder(t *testing.T) {
	_, alone := testConfig().Filter(ManualSelection("node-restart || backup-restore"), testSpecs())
	if !reflect.DeepEqual(alone, []string{"backup-restore", "node-restart"}) {
		t.Errorf("got %q", alone)
	}
}

func TestFilterManualAddsNoAlways(t *testing.T) {
	main, _ := testConfig().Filter(ManualSelection("logs"), testSpecs())
	if main != "logs && !alone" {
		t.Errorf("got %q", main)
	}
}

func TestFilterKeepsCompoundTerms(t *testing.T) {
	filters := func(string) ([]string, error) { return []string{"security-logs && logs"}, nil }
	main, _ := selectFilter(t, testConfig(), []string{"test/e2e/security_logs_test.go"}, filters)
	if main != "((security-logs && logs) || smoke) && !alone" {
		t.Errorf("got %q", main)
	}
}

func TestCheckManual(t *testing.T) {
	c := testConfig()
	for filter, ok := range map[string]bool{
		"logs":                 true,
		"logs || node-restart": true,
		"node-restart":         true,
		"logs ||":              false, // does not parse
		"secrets":              false, // a label outside labels:
		"proof":                false, // the sandbox runs no proof spec
	} {
		if err := c.CheckManual(filter, testSpecs()); (err == nil) != ok {
			t.Errorf("%q: error %v", filter, err)
		}
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	good := writeFile(t, dir, "good.yaml", `skip: ["docs/**"]
labels: [sandbox, smoke, logs, node-restart, alone]
always: smoke
alone: [node-restart]
full: smoke || node-restart
groups:
  - paths: ["system/loki/**"]
    tests: logs
components: ["system/*/"]
`)
	c, err := Load(good)
	if err != nil {
		t.Fatal(err)
	}
	if c.Groups[0].Tests != "logs" || c.Components[0] != "system/*/" || len(c.Labels) != 5 {
		t.Errorf("loaded %+v", c)
	}
	for name, body := range map[string]string{
		"unknown key":   "labels: [smoke]\nfull: smoke\nunknown: 1\n",
		"no full":       "labels: [smoke]\nalways: smoke\n",
		"no labels":     "full: smoke\n",
		"group no path": "labels: [smoke]\nfull: smoke\ngroups:\n  - tests: smoke\n",
		"bad filter":    "labels: [smoke]\nfull: smoke\ngroups:\n  - paths: [a]\n    tests: \"smoke ||\"\n",
		"bad glob":      "labels: [smoke]\nfull: smoke\nskip: [\"docs/[\"]\n",
	} {
		_, err := Load(writeFile(t, dir, strings.ReplaceAll(name, " ", "-")+".yaml", body))
		if err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("a missing file loaded")
	}
}

const logsSpecs = `package e2e

import . "github.com/onsi/ginkgo/v2"

func helper() {}

var _ = Describe("LogsB", Label("sandbox", "testbed", "logs"), func() {
	It("b", func() {})
})

var _ = Describe("SecurityLogsA", Ordered, Label("sandbox", "security-logs", "logs"), func() {})

var _ = Describe("NodeRestart", Serial, Label("sandbox", "node-restart", "alone"), func() {})
`

func TestSpecsReadsLabels(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/e2e/logs_test.go", logsSpecs)
	specs, err := Specs(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Spec{
		{Name: "LogsB", Labels: []string{"sandbox", "testbed", "logs"}, File: "test/e2e/logs_test.go"},
		{Name: "NodeRestart", Labels: []string{"sandbox", "node-restart", "alone"}, File: "test/e2e/logs_test.go"},
		{Name: "SecurityLogsA", Labels: []string{"sandbox", "security-logs", "logs"}, File: "test/e2e/logs_test.go"},
	}
	if !reflect.DeepEqual(specs, want) {
		t.Errorf("got %+v", specs)
	}
}

// A changed spec file selects each of its containers by the AND of its labels, the environment and alone left out
func TestFileTestsE2EContainers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/e2e/logs_test.go", logsSpecs)
	filters, err := FileTests(root)("test/e2e/logs_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(filters, []string{"logs", "security-logs && logs", "node-restart"}) {
		t.Errorf("got %q", filters)
	}
}

func TestFileTestsDeletedSpecFile(t *testing.T) {
	filters, err := FileTests(t.TempDir())("test/e2e/gone_test.go")
	if err != nil || filters != nil {
		t.Errorf("a deleted spec file: %q %v", filters, err)
	}
}

// A spec file's helpers used by other spec files select those files' containers too
func TestFileTestsIncludesHelperUsers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/e2e/sso_test.go", `package e2e

var timeout = 1

func secretValue() string { return "" }

var _ = Describe("SSOZot", Label("sandbox", "sso", "zot"), func() { _ = secretValue() })
`)
	writeFile(t, root, "test/e2e/ntfy_test.go", `package e2e

var _ = Describe("NtfyUsers", Label("sandbox", "ntfy"), func() { _ = secretValue() })
`)
	writeFile(t, root, "test/e2e/logs_test.go", `package e2e

var _ = Describe("LogsEvents", Label("sandbox", "logs"), func() { _ = timeout })
`)
	writeFile(t, root, "test/e2e/mesh_test.go", `package e2e

var _ = Describe("MeshEnrolled", Label("sandbox", "mesh"), func() {})
`)
	filters, err := FileTests(root)("test/e2e/sso_test.go")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(filters)
	if !reflect.DeepEqual(filters, []string{"logs", "ntfy", "sso && zot"}) {
		t.Errorf("got %q", filters)
	}
}

const envImporter = "package e2e\n\nimport \"github.com/duongle-steven/homelab/test/internal/env\"\n\nvar _ = Describe(\"A\", Label(\"sandbox\", \"identity\"), func() { _ = env.Name() })\n"

// A changed test/internal package selects the containers whose files import it
func TestFileTestsInternalPackage(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/internal/env/env.go", "package env\n\nfunc Name() string { return \"\" }\n")
	writeFile(t, root, "test/e2e/a_test.go", envImporter)
	// B names its own Name: only the import counts
	writeFile(t, root, "test/e2e/b_test.go", "package e2e\n\nfunc Name() string { return \"\" }\n\nvar _ = Describe(\"B\", Label(\"sandbox\", \"logs\"), func() { _ = Name() })\n")
	filters, err := FileTests(root)("test/internal/env/env.go")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(filters, []string{"identity"}) {
		t.Errorf("got %q", filters)
	}
}

// A deleted file of a test/internal package selects through the package's importers, as a changed one does
func TestFileTestsInternalDeletedFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/internal/env/env.go", "package env\n\nfunc Name() string { return \"\" }\n")
	writeFile(t, root, "test/e2e/a_test.go", envImporter)
	filters, err := FileTests(root)("test/internal/env/gone.go")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(filters, []string{"identity"}) {
		t.Errorf("got %q", filters)
	}
}

// ... and through another test/internal package that imports it
func TestFileTestsInternalTransitive(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/internal/env/env.go", "package env\n\nfunc Name() string { return \"\" }\n")
	writeFile(t, root, "test/internal/kube/kube.go", "package kube\n\nimport \"github.com/duongle-steven/homelab/test/internal/env\"\n\nfunc Config() string { return env.Name() }\n")
	writeFile(t, root, "test/e2e/c_test.go", "package e2e\n\nimport \"github.com/duongle-steven/homelab/test/internal/kube\"\n\nvar _ = Describe(\"C\", Label(\"sandbox\", \"mesh\"), func() { _ = kube.Config() })\n")
	filters, err := FileTests(root)("test/internal/env/env.go")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(filters, []string{"mesh"}) {
		t.Errorf("got %q", filters)
	}
}

func TestSelectInternalPackage(t *testing.T) {
	filters := func(file string) ([]string, error) {
		if file != "test/internal/env/env.go" {
			t.Fatalf("unexpected file %s", file)
		}
		return []string{"identity"}, nil
	}
	if main, _ := selectFilter(t, testConfig(), []string{"test/internal/env/env.go"}, filters); main != "(identity || smoke) && !alone" {
		t.Errorf("got %q", main)
	}
}

// An importer that holds only helpers passes the change on to the containers that use those helpers
func TestFileTestsInternalThroughHelperFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/internal/identity/identity.go", "package identity\n\nfunc PKCEPair() string { return \"\" }\n")
	writeFile(t, root, "test/e2e/identity_helpers_test.go", "package e2e\n\nimport \"github.com/duongle-steven/homelab/test/internal/identity\"\n\nfunc login() string { return identity.PKCEPair() }\n")
	writeFile(t, root, "test/e2e/sso_test.go", "package e2e\n\nvar _ = Describe(\"SSOGrafana\", Label(\"sandbox\", \"sso\", \"grafana\"), func() { _ = login() })\n")
	writeFile(t, root, "test/e2e/mesh_test.go", "package e2e\n\nvar _ = Describe(\"MeshEnrolled\", Label(\"sandbox\", \"mesh\"), func() {})\n")
	filters, err := FileTests(root)("test/internal/identity/identity.go")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(filters, []string{"sso && grafana"}) {
		t.Errorf("got %q", filters)
	}
}
