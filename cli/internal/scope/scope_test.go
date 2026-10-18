package scope

import (
	"os"
	"path/filepath"
	"reflect"
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
