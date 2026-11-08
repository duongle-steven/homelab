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
		Always: "Smoke",
		Alone:  []string{"NodeRestart"},
		Full:   "Smoke|Identity|Logs|NodeRestart",
		Groups: []Group{
			{Paths: []string{"Makefile"}, Tests: FullKeyword},
			{Paths: []string{"system/loki/**"}, Tests: "Logs"},
			{Paths: []string{"system/istio-system/**"}, Tests: "Identity|NodeRestart"},
			{Paths: []string{"system/kured/**"}, Tests: ""},
		},
	}
}

func noTests(string) ([]string, error) { return nil, nil }

func selectRegex(t *testing.T, c Config, files []string, names TestNames) (string, string) {
	t.Helper()
	s, err := c.Select(files, names)
	if err != nil {
		t.Fatal(err)
	}
	return c.Regex(s, false), c.Regex(s, true)
}

func TestSelect(t *testing.T) {
	c := testConfig()
	for _, tc := range []struct {
		name        string
		files       []string
		main, alone string
	}{
		{"only skipped", []string{"docs/a.md", "README.md"}, "", ""},
		{"one group", []string{"system/loki/values.yaml"}, "Logs|Smoke", ""},
		{"two groups", []string{"system/loki/values.yaml", "system/istio-system/values.yaml"}, "Identity|Logs|Smoke", "NodeRestart"},
		{"group without tests", []string{"system/kured/values.yaml"}, "Smoke", ""},
		{"unknown file", []string{"scripts/new"}, "Identity|Logs|Smoke", "NodeRestart"},
		{"full group", []string{"Makefile", "docs/a.md"}, "Identity|Logs|Smoke", "NodeRestart"},
		{"no changes", nil, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			main, alone := selectRegex(t, c, tc.files, noTests)
			if main != tc.main || alone != tc.alone {
				t.Errorf("got main %q alone %q, want %q %q", main, alone, tc.main, tc.alone)
			}
		})
	}
}

func TestSelectTestFile(t *testing.T) {
	names := func(file string) ([]string, error) {
		if file != "test/logs_test.go" {
			t.Fatalf("unexpected file %s", file)
		}
		return []string{"TestLogsPodLines", "TestLogsEvents"}, nil
	}
	main, alone := selectRegex(t, testConfig(), []string{"test/logs_test.go"}, names)
	if main != "Smoke|TestLogsEvents|TestLogsPodLines" || alone != "" {
		t.Errorf("got %q %q", main, alone)
	}
}

func TestSelectTestFileAlone(t *testing.T) {
	names := func(string) ([]string, error) { return []string{"TestNodeRestart", "TestNodeRestartBlockers"}, nil }
	main, alone := selectRegex(t, testConfig(), []string{"test/node_restart_test.go"}, names)
	if main != "Smoke" || alone != "TestNodeRestart|TestNodeRestartBlockers" {
		t.Errorf("got %q %q", main, alone)
	}
}

func TestManualSelection(t *testing.T) {
	c := testConfig()
	s := ManualSelection("Logs|NodeRestart")
	if got := c.Regex(s, false); got != "Logs" {
		t.Errorf("main %q: a manual regex adds no always", got)
	}
	if got := c.Regex(s, true); got != "NodeRestart" {
		t.Errorf("alone %q", got)
	}
}

func TestFullSelection(t *testing.T) {
	c := testConfig()
	s := Selection{Full: true}
	if got := c.Regex(s, false); got != "Identity|Logs|Smoke" {
		t.Errorf("main %q", got)
	}
	if got := c.Regex(s, true); got != "NodeRestart" {
		t.Errorf("alone %q", got)
	}
}

func TestSplitTerms(t *testing.T) {
	got := SplitTerms("Smoke|TestIdentity(TOTP|PKCE)|SSO")
	want := []string{"Smoke", "TestIdentity(TOTP|PKCE)", "SSO"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
	if SplitTerms("") != nil {
		t.Error("an empty regex has no terms")
	}
}

func TestAnchored(t *testing.T) {
	for term, want := range map[string]string{
		"Rotation":                 "^Test(Rotation)$",
		"TestNodeRestart":          "^Test(NodeRestart)$",
		"Rotation/forgejo":         "^Test(Rotation)$/forgejo",
		"Rotation/(scheduled|all)": "^Test(Rotation)$/(scheduled|all)",
		"Node(Restart|PowerCut)":   "^Test(Node(Restart|PowerCut))$",
		"^TestBackupRestore$":      "^TestBackupRestore$",
	} {
		if got := Anchored(term); got != want {
			t.Errorf("%s: got %q, want %q", term, got, want)
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
always: Smoke
alone: [NodeRestart]
full: Smoke|NodeRestart
groups:
  - paths: ["system/loki/**"]
    tests: Logs
components: ["system/*/"]
`)
	c, err := Load(good)
	if err != nil {
		t.Fatal(err)
	}
	if c.Groups[0].Tests != "Logs" || c.Components[0] != "system/*/" {
		t.Errorf("loaded %+v", c)
	}
	for name, body := range map[string]string{
		"unknown key":   "full: Smoke\nunknown: 1\n",
		"no full":       "always: Smoke\n",
		"group no path": "full: Smoke\ngroups:\n  - tests: Logs\n",
		"bad regex":     "full: Smoke\ngroups:\n  - paths: [a]\n    tests: \"Logs(\"\n",
		"bad glob":      "full: Smoke\nskip: [\"docs/[\"]\n",
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

func TestFileTests(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/logs_test.go", `package test

import "testing"

func TestLogsB(t *testing.T) {}
func TestLogsA(t *testing.T) {}
func helper() {}
`)
	names, err := FileTests(root)("test/logs_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"TestLogsB", "TestLogsA"}) {
		t.Errorf("got %q", names)
	}
	all, err := AllTests(root)
	if err != nil || !reflect.DeepEqual(all, []string{"TestLogsA", "TestLogsB"}) {
		t.Errorf("AllTests: %q %v", all, err)
	}
}

func TestFileTestsDeletedFile(t *testing.T) {
	names, err := FileTests(t.TempDir())("test/gone_test.go")
	if err != nil || names != nil {
		t.Errorf("a deleted test file: %q %v", names, err)
	}
}

// A test file's helpers used by other test files select those files' tests too
func TestFileTestsIncludesHelperUsers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/sso_test.go", "package test\n\nimport \"testing\"\n\nvar timeout = 1\n\nfunc secretValue() string { return \"\" }\nfunc TestSSOZot(t *testing.T) { _ = secretValue() }\n")
	writeFile(t, root, "test/ntfy_test.go", "package test\n\nimport \"testing\"\n\nfunc TestNtfyUsers(t *testing.T) { _ = secretValue() }\n")
	writeFile(t, root, "test/logs_test.go", "package test\n\nimport \"testing\"\n\nfunc TestLogsEvents(t *testing.T) { _ = timeout }\n")
	writeFile(t, root, "test/mesh_test.go", "package test\n\nimport \"testing\"\n\nfunc TestMeshEnrolled(t *testing.T) {}\n")
	names, err := FileTests(root)("test/sso_test.go")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"TestLogsEvents", "TestNtfyUsers", "TestSSOZot"}) {
		t.Errorf("got %q", names)
	}
}

func TestCheckManual(t *testing.T) {
	c := testConfig()
	tests := []string{"TestLogsPodLines", "TestNodeRestart", "TestSmoke"}
	for regex, ok := range map[string]bool{
		"Logs":             true,
		"Logs|NodeRestart": true,
		`Logs\|Secrets`:    false, // a literal |: matches nothing
		"Nothing":          false,
		".":                false, // would run TestNodeRestart with the others
		"Restart":          false,
	} {
		if err := c.CheckManual(regex, tests); (err == nil) != ok {
			t.Errorf("%q: error %v", regex, err)
		}
	}
}

// A changed test/internal package selects the tests whose files import it
func TestFileTestsInternalPackage(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/internal/env/env.go", "package env\n\nfunc Name() string { return \"\" }\n")
	writeFile(t, root, "test/a_test.go", "package test\n\nimport (\n\t\"testing\"\n\n\t\"github.com/duongle-steven/homelab/test/internal/env\"\n)\n\nfunc TestA(t *testing.T) { _ = env.Name() }\n")
	// TestB names its own Name: only the import counts
	writeFile(t, root, "test/b_test.go", "package test\n\nimport \"testing\"\n\nfunc Name() string { return \"\" }\n\nfunc TestB(t *testing.T) { _ = Name() }\n")
	names, err := FileTests(root)("test/internal/env/env.go")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"TestA"}) {
		t.Errorf("got %q", names)
	}
}

// ... and through another test/internal package that imports it
func TestFileTestsInternalTransitive(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/internal/env/env.go", "package env\n\nfunc Name() string { return \"\" }\n")
	writeFile(t, root, "test/internal/kube/kube.go", "package kube\n\nimport \"github.com/duongle-steven/homelab/test/internal/env\"\n\nfunc Config() string { return env.Name() }\n")
	writeFile(t, root, "test/c_test.go", "package test\n\nimport (\n\t\"testing\"\n\n\t\"github.com/duongle-steven/homelab/test/internal/kube\"\n)\n\nfunc TestC(t *testing.T) { _ = kube.Config() }\n")
	names, err := FileTests(root)("test/internal/env/env.go")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"TestC"}) {
		t.Errorf("got %q", names)
	}
}

func TestSelectInternalPackage(t *testing.T) {
	names := func(file string) ([]string, error) {
		if file != "test/internal/env/env.go" {
			t.Fatalf("unexpected file %s", file)
		}
		return []string{"TestA"}, nil
	}
	if main, _ := selectRegex(t, testConfig(), []string{"test/internal/env/env.go"}, names); main != "Smoke|TestA" {
		t.Errorf("got %q", main)
	}
}

// An importer that holds only helpers passes the change on to the tests that use those helpers
func TestFileTestsInternalThroughHelperFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "test/internal/identity/identity.go", "package identity\n\nfunc PKCEPair() string { return \"\" }\n")
	writeFile(t, root, "test/identity_helpers_test.go", "package test\n\nimport \"github.com/duongle-steven/homelab/test/internal/identity\"\n\nfunc login() string { return identity.PKCEPair() }\n")
	writeFile(t, root, "test/sso_test.go", "package test\n\nimport \"testing\"\n\nfunc TestSSOGrafana(t *testing.T) { _ = login() }\n")
	writeFile(t, root, "test/mesh_test.go", "package test\n\nimport \"testing\"\n\nfunc TestMeshEnrolled(t *testing.T) {}\n")
	names, err := FileTests(root)("test/internal/identity/identity.go")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"TestSSOGrafana"}) {
		t.Errorf("got %q", names)
	}
}
