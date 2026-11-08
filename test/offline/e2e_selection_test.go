package offline

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/onsi/ginkgo/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// e2eSelection is fixtures/e2e-selection.json: which cluster tests each test/scopes.yaml group, full, always, alone
// term and recipe ran before the suite moved to Ginkgo (14f-2), the selection the labels must keep.
type e2eSelection struct {
	// The commit the record was generated from
	From string `json:"from"`
	// The 84 cluster tests, without Test
	Tests []string `json:"tests"`
	// Group (its first path), full, always, alone/<term> or recipe → the tests it ran
	Selection map[string][]string `json:"selection"`
}

func loadE2ESelection(t *testing.T) e2eSelection {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repo.Root(), "test/offline/fixtures/e2e-selection.json"))
	require.NoError(t, err)
	var record e2eSelection
	require.NoError(t, json.Unmarshal(body, &record))
	return record
}

func TestE2ESelectionRecord(t *testing.T) {
	record := loadE2ESelection(t)
	assert.NotEmpty(t, record.From)
	require.Len(t, record.Tests, 84)
	known := map[string]bool{}
	for _, name := range record.Tests {
		known[name] = true
	}
	covered := map[string]bool{}
	for key, names := range record.Selection {
		for _, name := range names {
			assert.True(t, known[name], "%s names %s, not a cluster test", key, name)
		}
		if key == "production" {
			continue
		}
		for _, name := range names {
			covered[name] = true
		}
	}
	for _, name := range record.Tests {
		if name == "ArgoCDCheck" {
			assert.False(t, covered[name], "ArgoCDCheck runs only against production")
			assert.Contains(t, record.Selection["production"], name)
			continue
		}
		assert.True(t, covered[name], "%s: no scope or recipe runs it", name)
	}
}

// e2eSpec is one spec of the suite's dry run: its top-level container and its labels (the containers' and its own)
type e2eSpec struct {
	Container string
	Labels    []string
}

// e2eDryRun lists the suite's specs from Ginkgo's own dry run and JSON report, every one of them (no filter)
func e2eDryRun(t *testing.T) []e2eSpec {
	t.Helper()
	report := filepath.Join(t.TempDir(), "report.json")
	cmd := exec.Command("go", "test", "./e2e", "-run", "^TestE2E$", "-count=1", "-args", "-ginkgo.dry-run", "-ginkgo.json-report="+report)
	cmd.Dir = filepath.Join(repo.Root(), "test")
	cmd.Env = append(os.Environ(), "ENV=dev")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	raw, err := os.ReadFile(report)
	require.NoError(t, err)
	var reports []types.Report
	require.NoError(t, json.Unmarshal(raw, &reports))
	require.Len(t, reports, 1)
	var specs []e2eSpec
	for _, r := range reports[0].SpecReports {
		if r.LeafNodeType != types.NodeTypeIt {
			continue
		}
		require.NotEmpty(t, r.ContainerHierarchyTexts, r.LeafNodeText)
		spec := e2eSpec{Container: r.ContainerHierarchyTexts[0], Labels: slices.Clone(r.LeafNodeLabels)}
		for _, labels := range r.ContainerHierarchyLabels {
			spec.Labels = append(spec.Labels, labels...)
		}
		specs = append(specs, spec)
	}
	return specs
}

// selectContainers lists, sorted, the containers with a spec that filter && env selects
func selectContainers(t *testing.T, specs []e2eSpec, filter, env string) []string {
	t.Helper()
	selected, err := types.ParseLabelFilter("(" + filter + ") && " + env)
	require.NoError(t, err, filter)
	set := map[string]bool{}
	for _, s := range specs {
		if selected(s.Labels) {
			set[s.Container] = true
		}
	}
	var names []string
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// The labels select what the names selected before 14f-2 (fixtures/e2e-selection.json), for every scope group, full,
// always, each alone run and every recipe, on the environment each runs against
func TestE2EKeepsSelection(t *testing.T) {
	record := loadE2ESelection(t)
	raw, err := os.ReadFile(filepath.Join(repo.Root(), "test", "scopes.yaml"))
	require.NoError(t, err)
	var scopes struct {
		Always string `yaml:"always"`
		Full   string `yaml:"full"`
		Groups []struct {
			Paths []string `yaml:"paths"`
			Tests string   `yaml:"tests"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &scopes))
	// A group's run is its filter, the always filter and the alone runs it reaches: on the sandbox, all its filter selects
	group := func(tests string) string {
		switch tests {
		case "full":
			return scopes.Full
		case "":
			return scopes.Always
		}
		return "(" + tests + ") || " + scopes.Always
	}
	type run struct{ filter, env string }
	runs := map[string]run{
		"full":                                {scopes.Full, "sandbox"},
		"always":                              {scopes.Always, "sandbox"},
		"alone/NodeRestart":                   {"node-restart", "sandbox"},
		"alone/NodePowerCut":                  {"node-power-cut", "sandbox"},
		"alone/BackupRestore":                 {"backup-restore", "sandbox"},
		"alone/Rotation":                      {"rotation", "sandbox"},
		"testbed/proof":                       {"proof", "testbed"},
		"testbed/power/PowerCutEveryNode":     {"cut-every-node", "testbed"},
		"testbed/power/PowerShutdownStartup":  {"shutdown-startup", "testbed"},
		"testbed/power/PowerNodeNotReturning": {"node-not-returning", "testbed"},
		"testbed/power/PowerCutPostgres":      {"cut-postgres", "testbed"},
		"testbed/scan":                        {"scan", "testbed"},
		"sandbox/scan":                        {"scan", "sandbox"},
		"production":                          {"production", "production"},
	}
	for _, g := range scopes.Groups {
		runs[g.Paths[0]] = run{group(g.Tests), "sandbox"}
	}
	specs := e2eDryRun(t)
	for key, want := range record.Selection {
		r, ok := runs[key]
		if !assert.True(t, ok, "%s: no filter for it", key) {
			continue
		}
		assert.Equal(t, want, selectContainers(t, specs, r.filter, r.env), "%s: (%s) && %s", key, r.filter, r.env)
	}
	for key := range runs {
		_, ok := record.Selection[key]
		assert.True(t, ok, "%s: not in the record", key)
	}
}

// topLevelContainers parses test/e2e: the top-level `var _ = Describe(...)` calls, with each one's text, and every
// Label call outside their argument lists
func topLevelContainers(t *testing.T) (names []string, strayLabels []string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repo.Root(), "test", "e2e", "*_test.go"))
	require.NoError(t, err)
	for _, path := range files {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		allowed := map[*ast.CallExpr]bool{}
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Values) != 1 {
					continue
				}
				call, ok := value.Values[0].(*ast.CallExpr)
				if !ok {
					continue
				}
				if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "Describe" {
					continue
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				require.True(t, ok, "%s: a Describe's text is not a literal", fset.Position(call.Pos()))
				name, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				names = append(names, name)
				labelled := false
				for _, arg := range call.Args[1:] {
					if label, ok := arg.(*ast.CallExpr); ok {
						if id, ok := label.Fun.(*ast.Ident); ok && id.Name == "Label" {
							allowed[label], labelled = true, true
							for _, a := range label.Args {
								_, isLit := a.(*ast.BasicLit)
								assert.True(t, isLit, "%s: %s's labels must be string literals", fset.Position(label.Pos()), name)
							}
						}
					}
				}
				assert.True(t, labelled, "%s has no labels", name)
			}
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "Label" && !allowed[call] {
					strayLabels = append(strayLabels, fset.Position(call.Pos()).String())
				}
			}
			return true
		})
	}
	return names, strayLabels
}

// Labels sit only on the top-level containers: lab test scope reads them there, from the source
func TestE2ELabelsOnlyOnContainers(t *testing.T) {
	_, stray := topLevelContainers(t)
	assert.Empty(t, stray, "Label outside a top-level Describe")
}

// Each of the 84 cluster tests is one top-level container named after it, once
func TestE2EOneContainerPerOldTest(t *testing.T) {
	names, _ := topLevelContainers(t)
	sort.Strings(names)
	want := slices.Clone(loadE2ESelection(t).Tests)
	sort.Strings(want)
	assert.Equal(t, want, names)
	assert.Equal(t, strings.Join(names, " "), strings.Join(slices.Compact(slices.Clone(names)), " "), "a container name twice")
}
