package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testMap = `skip: ["docs/**"]
always: Smoke
alone: [NodeRestart]
full: Smoke|Logs|NodeRestart
groups:
  - paths: ["system/loki/**"]
    tests: Logs
components: ["system/*/"]
`

// repo makes a git repository with test/scopes.yaml and a test file on branch dev, then a branch feature.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write(t, dir, "test/scopes.yaml", testMap)
	write(t, dir, "test/logs_test.go", "package test\n\nimport \"testing\"\n\nfunc TestLogsPodLines(t *testing.T) {}\nfunc TestSmoke(t *testing.T) {}\nfunc TestNodeRestart(t *testing.T) {}\n")
	write(t, dir, "system/loki/values.yaml", "a: 1\n")
	run("init", "-q", "-b", "dev")
	run("add", ".")
	run("commit", "-q", "-m", "base")
	run("checkout", "-q", "-b", "feature")
	t.Chdir(dir)
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir string, args ...string) {
	t.Helper()
	for _, a := range [][]string{append([]string{"add", "-A"}, args...), {"commit", "-q", "-m", "change"}} {
		cmd := exec.Command("git", a...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
}

func lab(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := newRootCmd()
	cmd.SetArgs(append([]string{"test", "scope"}, args...))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String() + errOut.String(), err
}

func TestCommandSelects(t *testing.T) {
	dir := repo(t)
	write(t, dir, "system/loki/values.yaml", "a: 2\n")
	commit(t, dir)
	out, err := lab(t, "--base", "dev")
	if err != nil || out != "Logs|Smoke\n" {
		t.Errorf("got %q %v", out, err)
	}
	out, err = lab(t, "--base", "dev", "--alone")
	if err != nil || out != "\n" {
		t.Errorf("alone: got %q %v", out, err)
	}
}

func TestCommandNoChanges(t *testing.T) {
	repo(t)
	out, err := lab(t, "--base", "dev")
	if err != nil || out != "\n" {
		t.Errorf("got %q %v", out, err)
	}
}

func TestCommandFullAndManual(t *testing.T) {
	repo(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--full"}, "Logs|Smoke\n"},
		{[]string{"--full", "--alone"}, "NodeRestart\n"},
		{[]string{"--tests", "Logs|NodeRestart"}, "Logs\n"},
		{[]string{"--tests", "Logs|NodeRestart", "--alone"}, "NodeRestart\n"},
	} {
		out, err := lab(t, tc.args...)
		if err != nil || out != tc.want {
			t.Errorf("%v: got %q %v", tc.args, out, err)
		}
	}
}

func TestCommandMissingBase(t *testing.T) {
	repo(t)
	out, err := lab(t, "--base", "origin/dev")
	if err == nil || !strings.Contains(err.Error(), "git fetch origin dev") {
		t.Errorf("got %q %v", out, err)
	}
}

func TestCommandRenameCountsBothPaths(t *testing.T) {
	dir := repo(t)
	if err := os.Rename(filepath.Join(dir, "system/loki"), filepath.Join(dir, "system/logs")); err != nil {
		t.Fatal(err)
	}
	commit(t, dir)
	out, err := lab(t, "--base", "dev")
	// system/logs is unknown (full), system/loki is the Logs group: the union is the whole suite
	if err != nil || out != "Logs|Smoke\n" {
		t.Errorf("got %q %v", out, err)
	}
	out, _ = lab(t, "--base", "dev", "--alone")
	if out != "NodeRestart\n" {
		t.Errorf("alone: got %q, a rename into an unknown path runs everything", out)
	}
}

func TestCommandIgnoresWorkingTree(t *testing.T) {
	dir := repo(t)
	write(t, dir, "system/loki/values.yaml", "uncommitted: true\n")
	out, err := lab(t, "--base", "dev")
	if err != nil || out != "\n" {
		t.Errorf("got %q %v: only committed changes count", out, err)
	}
}

func TestCommandCheck(t *testing.T) {
	dir := repo(t)
	if out, err := lab(t, "--check"); err != nil {
		t.Errorf("clean map: %q %v", out, err)
	}
	write(t, dir, "system/newthing/values.yaml", "a: 1\n")
	commit(t, dir)
	out, err := lab(t, "--check")
	if err == nil || !strings.Contains(out, "system/newthing: in no group") {
		t.Errorf("got %q %v", out, err)
	}
}

func TestCommandInvalidMap(t *testing.T) {
	dir := repo(t)
	write(t, dir, "test/scopes.yaml", "full: Smoke\nunknown: 1\n")
	commit(t, dir)
	if out, err := lab(t, "--base", "dev"); err == nil {
		t.Errorf("an invalid map loaded: %q", out)
	}
}

func TestCommandManualRefusesUselessRegex(t *testing.T) {
	repo(t)
	for _, regex := range []string{`Logs\|Smoke`, "Nothing", "."} {
		if out, err := lab(t, "--tests", regex); err == nil {
			t.Errorf("%q accepted: %q", regex, out)
		}
	}
}

func labCharts(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetArgs(append([]string{"test", "charts"}, args...))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestChartsCommand(t *testing.T) {
	dir := repo(t)
	write(t, dir, "system/loki/Chart.yaml", "name: loki\n")
	write(t, dir, "system/kured/Chart.yaml", "name: kured\n")
	commit(t, dir)
	out, err := labCharts(t, "--base", "dev")
	if err != nil || out != "system/kured\nsystem/loki\n" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestChartsZeroBaseRendersAll(t *testing.T) {
	dir := repo(t)
	write(t, dir, "system/loki/Chart.yaml", "name: loki\n")
	commit(t, dir)
	out, err := labCharts(t, "--base", "0000000000000000000000000000000000000000")
	if err != nil || out != "system/loki\n" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}
