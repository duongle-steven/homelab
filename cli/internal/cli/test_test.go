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
labels: [sandbox, alone, smoke, logs, node-restart]
always: smoke
alone: [node-restart]
full: smoke || logs || node-restart
groups:
  - paths: ["system/loki/**"]
    tests: logs
components: ["system/*/"]
`

const testSpecs = "package e2e\n\n" +
	"var _ = Describe(\"LogsPodLines\", Label(\"sandbox\", \"logs\"), func() {})\n" +
	"var _ = Describe(\"Smoke\", Label(\"sandbox\", \"smoke\"), func() {})\n" +
	"var _ = Describe(\"NodeRestart\", Serial, Label(\"sandbox\", \"node-restart\", \"alone\"), func() {})\n"

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
	write(t, dir, "test/e2e/logs_test.go", testSpecs)
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
	if err != nil || out != "(logs || smoke) && !alone\n" {
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
		{[]string{"--full"}, "(logs || node-restart || smoke) && !alone\n"},
		{[]string{"--full", "--alone"}, "node-restart\n"},
		{[]string{"--tests", "logs || node-restart"}, "(logs || node-restart) && !alone\n"},
		{[]string{"--tests", "logs || node-restart", "--alone"}, "node-restart\n"},
		{[]string{"--full", "--alone", "--each"}, "node-restart\n"},
		{[]string{"--tests", "logs", "--alone", "--each"}, ""},
		// Only alone specs: the main run prints nothing (a filter selecting nothing fails --fail-on-empty)
		{[]string{"--tests", "node-restart"}, "\n"},
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
	// system/logs is unknown (full), system/loki is the logs group: the union is the whole suite
	if err != nil || out != "(logs || node-restart || smoke) && !alone\n" {
		t.Errorf("got %q %v", out, err)
	}
	out, _ = lab(t, "--base", "dev", "--alone")
	if out != "node-restart\n" {
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
	write(t, dir, "test/scopes.yaml", "labels: [smoke]\nfull: smoke\nunknown: 1\n")
	commit(t, dir)
	if out, err := lab(t, "--base", "dev"); err == nil {
		t.Errorf("an invalid map loaded: %q", out)
	}
}

func TestCommandManualRefusesUselessFilter(t *testing.T) {
	repo(t)
	for _, filter := range []string{"logs ||", "nothing", "Logs|Smoke"} {
		if out, err := lab(t, "--tests", filter); err == nil {
			t.Errorf("%q accepted: %q", filter, out)
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

// The basic checks run before a commit: uncommitted and untracked chart files count
func TestChartsCommandIncludesWorkingTree(t *testing.T) {
	dir := repo(t)
	write(t, dir, "system/loki/Chart.yaml", "name: loki\n")
	write(t, dir, "system/kured/Chart.yaml", "name: kured\n")
	commit(t, dir)
	write(t, dir, "system/kured/values.yaml", "a: 2\n") // untracked
	write(t, dir, "system/loki/values.yaml", "a: 3\n")  // tracked on dev, modified, not staged
	out, err := labCharts(t, "--base", "HEAD")
	if err != nil || out != "system/kured\nsystem/loki\n" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

// A chart removed on the branch shows up in git diff but is not rendered
func TestChartsCommandSkipsDeletedChart(t *testing.T) {
	dir := repo(t)
	write(t, dir, "system/loki/Chart.yaml", "name: loki\n")
	commit(t, dir)
	if err := os.RemoveAll(filepath.Join(dir, "system/loki")); err != nil {
		t.Fatal(err)
	}
	commit(t, dir)
	out, err := labCharts(t, "--base", "dev")
	if err != nil || out != "" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}
