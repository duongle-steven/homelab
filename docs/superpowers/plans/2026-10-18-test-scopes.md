# Test Scopes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `make dev` runs only the sandbox tests a branch's changes can affect, chosen by `lab test scope` (the first
command of a Go CLI in `cli/`) from `test/scopes.yaml`, with full and from-scratch runs kept for merges and releases.

**Architecture:** A Go package `cli/internal/scope` loads the map, turns changed files into `go test -run` regexes
(main run and "alone" run) and checks that the map covers every component. A cobra command `lab test scope` wires it to
git and prints one line. The root `Makefile` asks it for the two regexes and runs them as today; `cli/Makefile` holds
the CLI's unit tests and the map check, both part of the basic checks.

**Tech Stack:** Go 1.26 (dev shell's go1.26.8), github.com/spf13/cobra v1.10.2, go.yaml.in/yaml/v3 v3.0.5,
github.com/bmatcuk/doublestar/v4 v4.10.3, POSIX make.

**Spec:** `docs/superpowers/specs/2026-10-18-test-scopes-design.md`

## Global Constraints

- CLI module `github.com/duongle-steven/homelab/cli`; upstream's layout: `cli/cmd/lab/main.go`, `cli/internal/cli`
  (cobra root `lab`, `SilenceUsage`, `SilenceErrors`, `Execute`), packages under `cli/internal/<area>`.
- `test/go.mod`'s module becomes `github.com/duongle-steven/homelab/test`.
- Pinned versions: cobra v1.10.2, go.yaml.in/yaml/v3 v3.0.5, doublestar/v4 v4.10.3 (latest stable on 2026-10-18).
- `lab test scope` prints exactly one line on stdout (possibly empty); sorted, deduplicated `|`-joined terms; errors go
  to stderr with a non-zero exit; never interactive.
- The map lives in `test/scopes.yaml`; groups name tests, not components; an unknown file selects `full`.
- `make dev` defaults: `scope=auto`, `base=origin/dev`; `tests=<regex>` overrides; an empty selection stops before the
  push; `alone` tests run after the main run.
- The Makefile runs the CLI with `go run ./cmd/lab` from `cli/` (no Nix packaging in this step).
- Branches from `dev`, PR into `dev`; basic checks, then `make dev`.

## Review Focus

1. **A regex with a top-level alternation inside parentheses** (`TestIdentity(TOTP|PKCE)`): splitting on `|` must keep
   the parenthesised group whole. Pinned in Task 1 (`TestSplitTerms`).
2. **A renamed or deleted file:** both the old and the new path count (`--no-renames`), and a deleted test file selects
   nothing for itself instead of failing to parse. Pinned in Task 3 (`TestCommandRenameCountsBothPaths`) and Task 1
   (`TestFileTestsDeletedFile`).
3. **A main-run term that also matches an alone test** (a group naming `Node` would run `TestNodeRestart` alongside the
   others): `--check` must refuse it. Pinned in Task 2 (`TestCheckMainTermPicksAloneTest`).
4. **The branch is `dev` itself, or the base is missing:** an empty diff prints nothing (the rule says to run
   `scope=full` after a merge); a missing base fails with the fix in the message. Pinned in Task 3
   (`TestCommandMissingBase`, `TestCommandNoChanges`).
5. **Uncommitted changes:** `make dev` already refuses them; the selector reads only committed changes
   (`<base>...HEAD`), which is what `make dev` pushes. Pinned in Task 3 (`TestCommandIgnoresWorkingTree`).

---

### Task 1: The `scope` package: load the map and select tests

**Files:**
- Create: `cli/go.mod`, `cli/go.sum` (by `go mod tidy`)
- Create: `cli/internal/scope/scope.go`
- Create: `cli/internal/scope/tests.go`
- Test: `cli/internal/scope/scope_test.go`

**Interfaces:**
- Produces:
  - `type Config struct { Skip []string; Always string; Alone []string; Full string; Groups []Group; Components []string }` (yaml keys `skip`, `always`, `alone`, `full`, `groups`, `components`)
  - `type Group struct { Paths []string; Tests string }` (yaml `paths`, `tests`)
  - `const FullKeyword = "full"`
  - `func Load(path string) (Config, error)`
  - `type Selection struct { Full, Any, Manual bool; Terms []string }`
  - `type TestNames func(file string) ([]string, error)`
  - `func (c Config) Select(files []string, names TestNames) (Selection, error)`
  - `func ManualSelection(regex string) Selection`
  - `func (c Config) Regex(s Selection, alone bool) string`
  - `func SplitTerms(regex string) []string`
  - `func FileTests(root string) TestNames`
  - `func AllTests(root string) ([]string, error)`

- [ ] **Step 1: Create the module**

```bash
mkdir -p cli/internal/scope cli/internal/cli cli/cmd/lab
cat > cli/go.mod <<'EOF'
module github.com/duongle-steven/homelab/cli

go 1.26
EOF
```

- [ ] **Step 2: Write the failing tests**

`cli/internal/scope/scope_test.go`:

```go
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
```

- [ ] **Step 3: Run them to see them fail**

Run: `direnv exec . sh -c 'cd cli && go test ./internal/scope/' 2>&1 | grep -v direnv | head -5`
Expected: FAIL to build: `undefined: Config` (and the other names).

- [ ] **Step 4: Write `cli/internal/scope/scope.go`**

```go
// Package scope picks the sandbox tests a change can affect, from test/scopes.yaml.
package scope

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v3"
)

// FullKeyword as a group's tests selects the whole suite.
const FullKeyword = "full"

// Config is test/scopes.yaml.
type Config struct {
	// Changes that run no sandbox test
	Skip []string `yaml:"skip"`
	// Tests added to every non-empty selection
	Always string `yaml:"always"`
	// Terms whose tests run after the others (they disturb the cluster)
	Alone []string `yaml:"alone"`
	// The whole suite, alone tests included
	Full   string  `yaml:"full"`
	Groups []Group `yaml:"groups"`
	// Globs of what the map must cover (a trailing slash names directories)
	Components []string `yaml:"components"`
}

// Group maps changed paths to tests: a regex of test names, "" (only Always) or FullKeyword.
type Group struct {
	Paths []string `yaml:"paths"`
	Tests string   `yaml:"tests"`
}

// Load reads and validates a map.
func Load(path string) (Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if c.Full == "" {
		return Config{}, fmt.Errorf("%s: full is empty", path)
	}
	globs := append(append([]string{}, c.Skip...), c.Components...)
	regexes := append([]string{c.Always, c.Full}, c.Alone...)
	for i, g := range c.Groups {
		if len(g.Paths) == 0 {
			return Config{}, fmt.Errorf("%s: group %d has no paths", path, i+1)
		}
		globs = append(globs, g.Paths...)
		if g.Tests != FullKeyword {
			regexes = append(regexes, g.Tests)
		}
	}
	for _, glob := range globs {
		if !doublestar.ValidatePattern(strings.TrimSuffix(glob, "/")) {
			return Config{}, fmt.Errorf("%s: invalid glob %q", path, glob)
		}
	}
	for _, r := range regexes {
		if _, err := regexp.Compile(r); err != nil {
			return Config{}, fmt.Errorf("%s: invalid regex %q: %w", path, r, err)
		}
	}
	return c, nil
}

// Selection is what a change runs: the whole suite (Full), or Terms plus Always when Any file was not skipped.
// A Manual selection (make dev tests=...) runs exactly its Terms.
type Selection struct {
	Full, Any, Manual bool
	Terms             []string
}

// TestNames lists the Test functions a changed test file defines (none for a deleted file).
type TestNames func(file string) ([]string, error)

// Select maps changed files (relative to the repository root) to a Selection: skipped files add nothing; a file in
// groups adds their tests; a test file in no group adds the tests it defines; any other file selects the whole suite.
func (c Config) Select(files []string, names TestNames) (Selection, error) {
	var s Selection
	for _, file := range files {
		if matchAny(c.Skip, file) {
			continue
		}
		s.Any = true
		matched := false
		for _, g := range c.Groups {
			if !matchAny(g.Paths, file) {
				continue
			}
			matched = true
			if g.Tests == FullKeyword {
				s.Full = true
			} else {
				s.Terms = append(s.Terms, SplitTerms(g.Tests)...)
			}
		}
		if matched {
			continue
		}
		if isTestFile(file) {
			defined, err := names(file)
			if err != nil {
				return Selection{}, err
			}
			s.Terms = append(s.Terms, defined...)
			continue
		}
		s.Full = true
	}
	return s, nil
}

// ManualSelection runs exactly the given regex.
func ManualSelection(regex string) Selection {
	return Selection{Any: true, Manual: true, Terms: SplitTerms(regex)}
}

// Regex returns the go test -run regex of the main run, or with alone set of the run after it; "" runs nothing.
func (c Config) Regex(s Selection, alone bool) string {
	var terms []string
	switch {
	case s.Full:
		terms = SplitTerms(c.Full)
	case s.Manual:
		terms = s.Terms
	case s.Any:
		terms = append(append([]string{}, s.Terms...), SplitTerms(c.Always)...)
	}
	seen := map[string]bool{}
	var out []string
	for _, term := range terms {
		if term == "" || seen[term] || c.isAlone(term) != alone {
			continue
		}
		seen[term] = true
		out = append(out, term)
	}
	sort.Strings(out)
	return strings.Join(out, "|")
}

// SplitTerms splits a regex on its top-level |, keeping parenthesised alternations whole.
func SplitTerms(regex string) []string {
	if regex == "" {
		return nil
	}
	var terms []string
	depth, start := 0, 0
	for i, r := range regex {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case '|':
			if depth == 0 {
				terms = append(terms, regex[start:i])
				start = i + 1
			}
		}
	}
	return append(terms, regex[start:])
}

func (c Config) isAlone(term string) bool {
	for _, a := range c.Alone {
		if strings.Contains(term, a) {
			return true
		}
	}
	return false
}

func (c Config) known(file string) bool {
	if matchAny(c.Skip, file) || isTestFile(file) {
		return true
	}
	for _, g := range c.Groups {
		if matchAny(g.Paths, file) {
			return true
		}
	}
	return false
}

func matchAny(globs []string, file string) bool {
	for _, glob := range globs {
		if ok, _ := doublestar.Match(glob, file); ok {
			return true
		}
	}
	return false
}

func isTestFile(file string) bool {
	ok, _ := doublestar.Match("test/*_test.go", file)
	return ok
}
```

- [ ] **Step 5: Write `cli/internal/scope/tests.go`**

```go
package scope

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileTests reads a test file's Test functions with go/parser; a file that no longer exists defines none.
func FileTests(root string) TestNames {
	return func(file string) ([]string, error) {
		path := filepath.Join(root, file)
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				names = append(names, fn.Name.Name)
			}
		}
		return names, nil
	}
}

// AllTests lists every Test function in test/*_test.go, sorted.
func AllTests(root string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(root, "test", "*_test.go"))
	if err != nil {
		return nil, err
	}
	var all []string
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		names, err := FileTests(root)(filepath.ToSlash(rel))
		if err != nil {
			return nil, err
		}
		all = append(all, names...)
	}
	sort.Strings(all)
	return all, nil
}
```

- [ ] **Step 6: Pin the dependencies and run the tests**

Run: `direnv exec . sh -c 'cd cli && go get github.com/bmatcuk/doublestar/v4@v4.10.3 go.yaml.in/yaml/v3@v3.0.5 && go mod tidy && go test ./internal/scope/' 2>&1 | grep -v direnv | tail -3`
Expected: `ok  	github.com/duongle-steven/homelab/cli/internal/scope`. `cli/go.mod` requires exactly those two versions.

- [ ] **Step 7: Commit**

```bash
git add cli/go.mod cli/go.sum cli/internal/scope
git commit -m "feat(cli): scope package, sandbox tests chosen from changed files"
```

---

### Task 2: The map check

**Files:**
- Create: `cli/internal/scope/check.go`
- Test: `cli/internal/scope/check_test.go`

**Interfaces:**
- Consumes: `Config`, `SplitTerms`, `Config.known`, `Config.isAlone`, `FullKeyword` (Task 1).
- Produces: `func (c Config) Check(tracked, tests []string) []string` (problems, sorted; empty when the map is complete).

- [ ] **Step 1: Write the failing tests**

`cli/internal/scope/check_test.go`:

```go
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
```

- [ ] **Step 2: Run them to see them fail**

Run: `direnv exec . sh -c 'cd cli && go test ./internal/scope/' 2>&1 | grep -v direnv | head -3`
Expected: FAIL to build: `c.Check undefined`.

- [ ] **Step 3: Write `cli/internal/scope/check.go`**

```go
package scope

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Check lists what the map misses: components with a tracked file that no group or skip knows (it would run the
// whole suite), regexes that match no test, and main-run terms that would also pick an alone test.
func (c Config) Check(tracked, tests []string) []string {
	var problems []string
	uncovered := map[string]bool{}
	for _, file := range tracked {
		if component := c.component(file); component != "" && !c.known(file) {
			uncovered[component] = true
		}
	}
	for component := range uncovered {
		problems = append(problems, component+": in no group of test/scopes.yaml (add it to a group, or to skip)")
	}
	sort.Strings(problems)

	var aloneTests []string
	for _, test := range tests {
		if c.isAlone(test) {
			aloneTests = append(aloneTests, test)
		}
	}
	check := func(where, term string) {
		re := regexp.MustCompile(term) // Load validated it
		if !matchesAny(re, tests) {
			problems = append(problems, fmt.Sprintf("%s: %q matches no test", where, term))
			return
		}
		if c.isAlone(term) {
			return
		}
		for _, test := range aloneTests {
			if re.MatchString(test) {
				problems = append(problems, fmt.Sprintf("%s: %q would run alone test %s with the others", where, term, test))
				return
			}
		}
	}
	for _, term := range SplitTerms(c.Always) {
		check("always", term)
	}
	for _, term := range SplitTerms(c.Full) {
		check("full", term)
	}
	for _, term := range c.Alone {
		check("alone", term)
	}
	for i, g := range c.Groups {
		if g.Tests == FullKeyword {
			continue
		}
		for _, term := range SplitTerms(g.Tests) {
			check(fmt.Sprintf("group %d", i+1), term)
		}
	}
	return problems
}

// component names the component a tracked file belongs to ("" for none): the file itself for a file glob, the
// matching leading directories for a directory glob (trailing slash).
func (c Config) component(file string) string {
	parts := strings.Split(file, "/")
	for _, glob := range c.Components {
		if !strings.HasSuffix(glob, "/") {
			if ok, _ := doublestar.Match(glob, file); ok {
				return file
			}
			continue
		}
		dirGlob := strings.TrimSuffix(glob, "/")
		depth := strings.Count(dirGlob, "/") + 1
		if len(parts) <= depth {
			continue
		}
		dir := strings.Join(parts[:depth], "/")
		if ok, _ := doublestar.Match(dirGlob, dir); ok {
			return dir
		}
	}
	return ""
}

func matchesAny(re *regexp.Regexp, names []string) bool {
	for _, name := range names {
		if re.MatchString(name) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the tests**

Run: `direnv exec . sh -c 'cd cli && go test ./internal/scope/' 2>&1 | grep -v direnv | tail -2`
Expected: `ok`.

- [ ] **Step 5: Commit**

```bash
git add cli/internal/scope/check.go cli/internal/scope/check_test.go
git commit -m "feat(cli): check that test/scopes.yaml covers every component and names real tests"
```

---

### Task 3: The `lab` command

**Files:**
- Create: `cli/internal/scope/git.go`
- Create: `cli/internal/cli/root.go`, `cli/internal/cli/test.go`
- Create: `cli/cmd/lab/main.go`
- Create: `cli/Makefile`
- Test: `cli/internal/cli/test_test.go`

**Interfaces:**
- Consumes: `Load`, `Config.Select`, `Config.Regex`, `Config.Check`, `ManualSelection`, `FileTests`, `AllTests`,
  `Selection` (Tasks 1–2).
- Produces:
  - `func Root() (string, error)`, `func Changed(root, base string) ([]string, error)`, `func Tracked(root string) ([]string, error)` in `scope`
  - `lab test scope [--base origin/dev] [--alone] [--full] [--tests <regex>] [--check] [--config test/scopes.yaml]`
  - `make -C cli test` (`go vet ./...`, `go test -race ./...`) and `make -C cli check` (`go run ./cmd/lab test scope --check`)

- [ ] **Step 1: Write the failing tests**

`cli/internal/cli/test_test.go`:

```go
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
```

- [ ] **Step 2: Run them to see them fail**

Run: `direnv exec . sh -c 'cd cli && go test ./internal/cli/' 2>&1 | grep -v direnv | head -3`
Expected: FAIL to build: `undefined: newRootCmd`.

- [ ] **Step 3: Write `cli/internal/scope/git.go`**

```go
package scope

import (
	"fmt"
	"os/exec"
	"strings"
)

// Root is the repository's top directory.
func Root() (string, error) {
	out, err := git("", "rev-parse", "--show-toplevel")
	return strings.TrimSpace(out), err
}

// Changed lists the files the commits since base change, old and new paths of renames included. Uncommitted changes
// do not count: make dev tests the commit at HEAD.
func Changed(root, base string) ([]string, error) {
	if _, err := git(root, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return nil, fmt.Errorf("base %s does not exist: git fetch origin dev, or make dev scope=full", base)
	}
	out, err := git(root, "diff", "--name-only", "--no-renames", base+"...HEAD")
	return lines(out), err
}

// Tracked lists the repository's tracked files.
func Tracked(root string) ([]string, error) {
	out, err := git(root, "ls-files")
	return lines(out), err
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func lines(out string) []string {
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}
```

- [ ] **Step 4: Write the command**

`cli/internal/cli/root.go`:

```go
// Package cli is the lab command (upstream nixos's CLI layout; its other commands come in a later step).
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "lab",
		Short:         "Homelab tools",
		Example:       "  lab test scope --base origin/dev",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newTestCmd())
	return root
}

// Execute runs the lab CLI and exits on failure.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		// A diagnostic-write failure cannot change the already-failed exit status.
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
```

`cli/internal/cli/test.go`:

```go
package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/duongle-steven/homelab/cli/internal/scope"
)

func newTestCmd() *cobra.Command {
	test := &cobra.Command{Use: "test", Short: "Choose and check the sandbox tests"}
	test.AddCommand(newTestScopeCmd())
	return test
}

func newTestScopeCmd() *cobra.Command {
	var base, tests, config string
	var alone, full, check bool
	cmd := &cobra.Command{
		Use:   "scope",
		Short: "Print the go test -run regex of the sandbox tests a change can affect",
		Long: "Reads test/scopes.yaml and the files changed since --base, and prints one line: the regex of the main run, " +
			"or with --alone of the tests that run after it; nothing when every change is skipped. --check verifies " +
			"that the map covers every component and names existing tests.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := scope.Root()
			if err != nil {
				return err
			}
			if !filepath.IsAbs(config) {
				config = filepath.Join(root, config)
			}
			c, err := scope.Load(config)
			if err != nil {
				return err
			}
			if check {
				tracked, err := scope.Tracked(root)
				if err != nil {
					return err
				}
				names, err := scope.AllTests(root)
				if err != nil {
					return err
				}
				problems := c.Check(tracked, names)
				for _, p := range problems {
					fmt.Fprintln(cmd.ErrOrStderr(), p)
				}
				if len(problems) > 0 {
					return fmt.Errorf("test/scopes.yaml: %d problem(s)", len(problems))
				}
				return nil
			}
			var s scope.Selection
			switch {
			case tests != "":
				s = scope.ManualSelection(tests)
			case full:
				s = scope.Selection{Full: true}
			default:
				files, err := scope.Changed(root, base)
				if err != nil {
					return err
				}
				if s, err = c.Select(files, scope.FileTests(root)); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), c.Regex(s, alone))
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&base, "base", "origin/dev", "compare HEAD with this ref (merge base)")
	flags.BoolVar(&alone, "alone", false, "print the tests that run alone, after the main run")
	flags.BoolVar(&full, "full", false, "the whole suite")
	flags.StringVar(&tests, "tests", "", "exactly these tests (a go test -run regex)")
	flags.BoolVar(&check, "check", false, "verify the map instead of printing a selection")
	flags.StringVar(&config, "config", "test/scopes.yaml", "the map, relative to the repository root")
	cmd.MarkFlagsMutuallyExclusive("full", "tests", "check")
	return cmd
}
```

`cli/cmd/lab/main.go`:

```go
package main

import "github.com/duongle-steven/homelab/cli/internal/cli"

func main() {
	cli.Execute()
}
```

`cli/Makefile`:

```make
.POSIX:
.PHONY: test check

# Part of the basic checks
test:
	go vet ./...
	go test -race ./...

# test/scopes.yaml covers every component and names existing tests (part of the basic checks)
check:
	go run ./cmd/lab test scope --check
```

- [ ] **Step 5: Pin cobra and run the tests**

Run: `direnv exec . sh -c 'cd cli && go get github.com/spf13/cobra@v1.10.2 && go mod tidy && make test' 2>&1 | grep -v direnv | tail -4`
Expected: `ok` for `internal/cli` and `internal/scope`; `go vet` silent.

- [ ] **Step 6: Commit**

```bash
git add cli
git commit -m "feat(cli): lab test scope, the first command of the lab CLI"
```

---

### Task 4: The map, and `test/`'s module path

**Files:**
- Create: `test/scopes.yaml`
- Modify: `test/go.mod:1`
- Test: `cli/internal/scope/repository_test.go`

**Interfaces:**
- Consumes: `Load`, `Config.Select`, `Config.Regex`, `FileTests` (Task 1); `make -C cli check` (Task 3).
- Produces: `test/scopes.yaml` (read by `make dev` in Task 5).

- [ ] **Step 1: Write the failing test against the real map**

`cli/internal/scope/repository_test.go`:

```go
package scope

import (
	"path/filepath"
	"strings"
	"testing"
)

// The repository's own map selects what the spec's table says
func TestRepositoryMap(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(filepath.Join(root, "test", "scopes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	full := "Egress|Forgejo|Gateway|Identity|Logs|Mesh|Monitoring|Ntfy|SSO|Secrets|Smoke"
	for _, tc := range []struct {
		file, main, alone string
	}{
		{"docs/index.md", "", ""},
		{"CLAUDE.md", "", ""},
		{"system/loki/values.yaml", "Logs|Smoke", ""},
		{"system/istio-system/values.yaml", "Egress|Gateway|Identity|Mesh|Smoke", "NodeRestart"},
		{"infra/root.hcl", "Identity|Ntfy|SSO|Secrets|Smoke", ""},
		{"platform/forgejo/values.yaml", "Forgejo|GatewayForgejoSSH|SSOForgejo|Smoke", ""},
		{"apps/paperless.yaml", "SSOPaperless|Smoke", ""},
		{"apps/blog.yaml", "Smoke", ""},
		{"system/kured/values.yaml", "Smoke", ""},
		{"test/state_rotation_test.go", "", ""},
		{"Makefile", full, "NodeRestart"},
		{"scripts/something-new", full, "NodeRestart"},
	} {
		s, err := c.Select([]string{tc.file}, FileTests(root))
		if err != nil {
			t.Fatal(err)
		}
		if main, alone := c.Regex(s, false), c.Regex(s, true); main != tc.main || alone != tc.alone {
			t.Errorf("%s: got %q %q, want %q %q", tc.file, main, alone, tc.main, tc.alone)
		}
	}
	s, err := c.Select([]string{"test/logs_test.go"}, FileTests(root))
	if err != nil {
		t.Fatal(err)
	}
	if main := c.Regex(s, false); !strings.Contains(main, "TestLogsPodLines") || !strings.Contains(main, "Smoke") {
		t.Errorf("test/logs_test.go: %q", main)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `direnv exec . sh -c 'cd cli && go test -run TestRepositoryMap ./internal/scope/' 2>&1 | grep -v direnv | head -3`
Expected: FAIL: `open .../test/scopes.yaml: no such file or directory`.

- [ ] **Step 3: Write `test/scopes.yaml`**

```yaml
# Which sandbox tests a change runs (lab test scope, docs/how-to-guides/sandbox-tests.md). Paths are globs (**
# spans directories) matched against the files a branch changes; a file no entry knows runs the whole suite, and
# `make -C cli check` fails until its component is added here.
skip:
  - docs/**
  - "**/*.md"
  - mkdocs.yml
  - LICENSE.md
  - renovate.json5
  - .github/**
  - .woodpecker/**
  - .gitignore
  - .dockerignore
  - .yamllint.yaml
  - .pre-commit-config.yaml
  # Offline tests and their scripts: the basic checks run them
  - test/state_*_test.go
  - test/cni_watchdog_test.go
  - test/apps_test.go
  - test/tools_test.go
  - scripts/check-*
  - scripts/render-chart
  - scripts/env-values
  - scripts/cloudflare-credentials
  - scripts/ensure-state-bucket
  - scripts/rotate-state-passphrase
  - scripts/helm-diff
  - scripts/__pycache__/**
  - cli/**
always: Smoke
alone: [NodeRestart]
full: Smoke|Identity|SSO|Gateway|Forgejo|Mesh|Monitoring|Egress|Secrets|Ntfy|Logs|NodeRestart
groups:
  - paths:
      - Makefile
      - test/Makefile
      - test/scopes.yaml
      - test/go.mod
      - test/go.sum
      - test/*helpers*_test.go
      - flake.nix
      - flake.lock
      - nix/**
      - .envrc
      - Dockerfile
      - system/argocd/**
      - infra/_modules/argocd/**
      - infra/sandbox/argocd/**
      - infra/_modules/k3d/k3d.yaml
    tests: full
  - paths:
      - system/istio-system/**
      - infra/_modules/k3d/**
      - infra/sandbox/cluster/**
    tests: Mesh|Gateway|Egress|Identity|NodeRestart
  - paths: [system/egress/**]
    tests: Egress
  - paths: [system/cert-manager/**]
    tests: Gateway|Identity|SSO
  - paths: [system/loki/**]
    tests: Logs
  - paths: [system/monitoring-system/**]
    tests: Monitoring|Mesh|Ntfy|Logs
  - paths:
      - system/secret-store/**
      - platform/openbao/**
      - platform/external-secrets/**
      - platform/global-secrets/**
      - platform/infra-secrets/**
      - infra/_modules/openbao/**
      - infra/_modules/secrets/**
      - infra/sandbox/openbao/**
      - infra/sandbox/secrets/**
      - infra/root.hcl
    tests: Secrets|Identity|SSO|Ntfy
  - paths: [platform/kanidm/**, scripts/onboard-user]
    tests: Identity|SSO
  - paths: [platform/grafana/**]
    tests: SSOGrafana|Logs|Monitoring
  - paths: [platform/forgejo/**]
    tests: Forgejo|SSOForgejo|GatewayForgejoSSH
  - paths: [platform/zot/**]
    tests: SSOZot|GatewayZotLargePush
  - paths: [apps/actualbudget.yaml]
    tests: SSOActualBudget
  - paths: [apps/paperless.yaml]
    tests: SSOPaperless
  - paths: [apps/opencloud.yaml]
    tests: SSOOpenCloud
  - paths: [apps/ntfy.yaml]
    tests: Ntfy
  # Smoke only: apps without their own tests, components the sandbox does not run, and tools for production
  - paths:
      - apps/blog.yaml
      - apps/excalidraw.yaml
      - apps/homepage.yaml
      - apps/jellyfin.yaml
      - apps/matrix/**
      - apps/ollama.yaml
      - apps/pairdrop.yaml
      - apps/speedtest.yaml
      - apps/tailscale.yaml
      - apps/wireguard.yaml
      - platform/cloudnative-pg/**
      - platform/renovate/**
      - platform/woodpecker/**
      - system/cloudflared/**
      - system/external-dns/**
      - system/kured/**
      - system/rook-ceph/**
      - system/volsync-system/**
      - infra/Makefile
      - infra/production/**
      - infra/_modules/cloudflare/**
      - metal/**
      - test/benchmark/**
      - scripts/argocd-admin-password
      - scripts/get-dns-config
      - scripts/get-status
      - scripts/get-wireguard-config
      - scripts/new-service
      - scripts/pxe-logs
      - scripts/take-screenshots
    tests: ""
components:
  - system/*/
  - platform/*/
  - apps/*.yaml
  - apps/*/
  - infra/_modules/*/
  - infra/*/*/
  - scripts/*
  - nix/*
```

- [ ] **Step 4: Rename `test/`'s module**

Replace the first line of `test/go.mod` with `module github.com/duongle-steven/homelab/test`.

- [ ] **Step 5: Run the repository test, the map check, and the offline tests**

Run: `direnv exec . sh -c 'cd cli && go test ./... && make check' 2>&1 | grep -v direnv | tail -3; direnv exec . sh -c "cd test && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady|TestGatewayNodeAddressFromInspect|TestNodeRestart(Blockers|WatchdogJobs)|TestIstioCNIWatchdog|TestState' ./..." 2>&1 | tail -1`
Expected: `ok` for both CLI packages, `make check` prints nothing and exits 0, then
`ok  	github.com/duongle-steven/homelab/test`. If `make check` names a component, add its paths to the right group
(a ruling in the ledger when the table above did not cover it).

- [ ] **Step 6: Commit**

```bash
git add test/scopes.yaml test/go.mod cli/internal/scope/repository_test.go
git commit -m "feat(test): test/scopes.yaml, the sandbox tests each change runs; test/'s module follows the fork"
```

---

### Task 5: `make dev` runs the selection

**Files:**
- Modify: `Makefile:1-20`

**Interfaces:**
- Consumes: `lab test scope` flags (Task 3), `test/scopes.yaml` (Task 4).
- Produces: `make dev [scope=auto|full] [base=<ref>] [tests=<regex>]`, `make dev-fresh`.

- [ ] **Step 1: Record the sandbox branch before the dry runs**

Run: `git ls-remote origin sandbox | cut -c1-12`
Expected: the commit `make dev` last pushed (note it).

- [ ] **Step 2: Rewrite the `dev` target**

Replace the variables block and the `dev` target in `Makefile` with:

```make
env ?= production
KUBECONFIG = $(shell pwd)/infra/$(env)/kubeconfig.yaml
KUBE_CONFIG_PATH = $(KUBECONFIG)
smoke_filter ?= Smoke
terragrunt_apply = terragrunt run --all apply --non-interactive -- -auto-approve
# make dev: the sandbox tests the branch's changes can affect (test/scopes.yaml), or scope=full, or tests=<regex>
scope ?= auto
base ?= origin/dev
tests ?=
lab = cd cli && go run ./cmd/lab test scope

default: metal infra smoke-test clean

# The sandbox syncs GitHub branch sandbox (system/argocd/values-dev.yaml): test the commit you are on
dev:
	@if [ -n "$(tests)" ]; then sel="--tests '$(tests)'"; \
	elif [ "$(scope)" = full ]; then sel=--full; \
	else git fetch -q origin dev && sel="--base $(base)"; fi; \
	main=$$(eval "$(lab) $$sel") && alone=$$(eval "$(lab) $$sel --alone") || exit 1; \
	if [ -z "$$main$$alone" ]; then echo "make dev: nothing to test in the sandbox (every change is in test/scopes.yaml's skip)"; exit 0; fi; \
	echo "make dev: main run '$$main', then alone '$$alone'"; \
	$(MAKE) sandbox-push && \
	(cd infra/sandbox && $(terragrunt_apply)) && \
	if [ -n "$$main" ]; then $(MAKE) env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter="$$main" sandbox-refresh smoke-test; fi && \
	if [ -n "$$alone" ]; then $(MAKE) env=sandbox ENV=dev INSECURE_SKIP_VERIFY=1 smoke_filter="$$alone" smoke-test; fi

# From scratch, the whole suite: before a dev → master PR and a release
dev-fresh:
	$(MAKE) dev-clean
	$(MAKE) dev scope=full
```

The alone run keeps today's comment: add `# Alone tests (TestNodeRestart restarts the node) run after the others` above
the last `if` line.

- [ ] **Step 3: Dry-run a skipped change**

Run (a throwaway branch, deleted afterwards):

```bash
git checkout -q -b scratch/scope-skip
echo "" >> docs/index.md && git commit -qam "scratch: docs only"
direnv exec . make dev base=HEAD~1 2>&1 | grep -v direnv | tail -2
git checkout -q feat/test-scopes && git branch -q -D scratch/scope-skip
git ls-remote origin sandbox | cut -c1-12
```

Expected: `make dev: nothing to test in the sandbox (...)`, and the sandbox branch is still the commit from Step 1.

- [ ] **Step 4: Dry-run a Loki-only change's selection**

Run:

```bash
git checkout -q -b scratch/scope-loki
echo "# scratch" >> system/loki/values.yaml && git commit -qam "scratch: loki"
direnv exec . sh -c 'cd cli && go run ./cmd/lab test scope --base HEAD~1; go run ./cmd/lab test scope --base HEAD~1 --alone' 2>&1 | grep -v direnv
git checkout -q feat/test-scopes && git branch -q -D scratch/scope-loki
```

Expected: `Logs|Smoke`, then an empty line.

- [ ] **Step 5: Commit**

```bash
git add Makefile
git commit -m "feat: make dev runs the sandbox tests the change can affect; make dev-fresh"
```

---

### Task 6: Documentation and rules

**Files:**
- Modify: `CLAUDE.md`
- Create: `docs/how-to-guides/sandbox-tests.md`
- Modify: `mkdocs.yml` (nav)

**Interfaces:** documentation only.

- [ ] **Step 1: CLAUDE.md**

- Roadmap: after step 13's line, step 14 becomes `14. Enhancements: 14a test scopes: make dev runs the sandbox tests the change can affect (test/scopes.yaml, lab test scope in cli/), done before steps 8d–13 (done); 14b go-task replaces make (...the existing text...)`.
- Rules, the sandbox/basic-checks bullet: the basic checks gain `make -C cli test check`; replace "then commit and run `make dev`, which force-pushes … before opening the PR; after the merge, run `make dev` on `dev` once more" with:
  "then commit and run `make dev`: it runs the sandbox tests the branch's changes can affect (`test/scopes.yaml`, `lab test scope --base origin/dev`), prints that there is nothing to test when every change is skipped, and otherwise force-pushes the commit to GitHub branch `sandbox` and runs them; its last run before the PR covers the whole branch. After the merge, run `make dev scope=full` on `dev` (skip it when every change was skipped). Before a `dev` → `master` PR or a release, run `make dev-fresh` (from scratch, the whole suite). `make dev tests='<regex>'` runs exactly those tests. A new component gets its entry in `test/scopes.yaml` in the same PR (`make -C cli check` fails otherwise; until then it runs everything)."
- Sandbox section, the `make dev` bullet: name `scope=full`, `tests=`, `make dev-fresh`, and that `lab` runs with `go run` from `cli/` (Go module `github.com/duongle-steven/homelab/cli`, upstream's layout; its other commands come with the CLI step).
- Ideas: the Go CLI entry starts "`cli/` exists with `lab test scope`;".

- [ ] **Step 2: `docs/how-to-guides/sandbox-tests.md`**

```markdown
# Sandbox tests

`make dev` builds or updates the sandbox and runs the tests the branch's changes can affect.

| Command | Runs |
|---|---|
| `make dev` | The tests `test/scopes.yaml` maps the branch's changes to (`git diff origin/dev...HEAD`), plus `Smoke`; nothing when every change is skipped (docs and the like); the whole suite when a file is in no entry |
| `make dev scope=full` | The whole suite on the existing sandbox: after merging into `dev` |
| `make dev-fresh` | `make dev-clean`, then the whole suite on a new sandbox: before a `dev` → `master` PR or a release |
| `make dev tests='Logs\|Secrets'` | Exactly those tests (a `go test -run` regex) |

`TestNodeRestart` restarts the node, so it always runs alone, after the others.

To see what a branch would run without running it:

```sh
cd cli && go run ./cmd/lab test scope            # the main run
cd cli && go run ./cmd/lab test scope --alone    # what runs alone afterwards
```

## Adding a component

Add its paths to a group in `test/scopes.yaml` (the tests that cover it), or to `skip`. `make -C cli check` (a basic
check) fails while a component is in no entry, and while a group names a test that does not exist.
```

Add `- Sandbox tests: how-to-guides/sandbox-tests.md` to `mkdocs.yml`'s how-to guides nav, next to the other entries
(match their format).

- [ ] **Step 3: Build the docs**

Run: `direnv exec . mkdocs build --strict 2>&1 | tail -1`
Expected: `Documentation built`, no warning.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md docs/how-to-guides/sandbox-tests.md mkdocs.yml
git commit -m "docs: test scopes, the rules for when the sandbox runs which tests"
```

---

### Task 7: Final checks and the first scoped `make dev`

**Files:** none.

- [ ] **Step 1: Basic checks**

Run: `direnv exec . sh -c "cd test && go vet ./... && go test -run 'TestSmokeAppsForEnv|TestIdentity(TOTPWaitsForNextStep|PKCE)|TestGatewayRouteReady|TestGatewayNodeAddressFromInspect|TestNodeRestart(Blockers|WatchdogJobs)|TestIstioCNIWatchdog|TestState' ./..." | tail -1; direnv exec . make -C cli test check >/dev/null && echo cli=0; for c in check-mesh-policies check-env-config check-secret-stores; do direnv exec . scripts/$c >/dev/null 2>&1; echo $c=$?; done; direnv exec . env -u HOMELAB_STATE_PASSPHRASE make -C infra check >/dev/null 2>&1; echo infra=$?; direnv exec . mkdocs build --strict >/dev/null 2>&1; echo mkdocs=$?`
Expected: `ok`, `cli=0`, every check `=0`, `infra=0`, `mkdocs=0`.

- [ ] **Step 2: `make dev` (the branch changes `Makefile`, so it selects the whole suite)**

Run: `direnv exec . make dev > /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/dev-scopes.log 2>&1; echo dev=$?; grep -aE '^make dev:|^DONE|PASS TestNodeRestart |=== FAIL' /tmp/claude-1000/-home-nixos-homelab/44339b87-568a-46fd-9484-aed834c50ef8/scratchpad/dev-scopes.log | tail -5`
Expected: `dev=0`; `make dev: main run 'Egress|Forgejo|Gateway|Identity|Logs|Mesh|Monitoring|Ntfy|SSO|Secrets|Smoke', then alone 'NodeRestart'`; `DONE 123 tests`; `TestNodeRestart` passes.
