// Package scope picks the sandbox specs a change can affect, from test/scopes.yaml, as Ginkgo label filters.
package scope

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/onsi/ginkgo/v2/types"
	"go.yaml.in/yaml/v3"
)

// FullKeyword as a group's tests selects the whole suite.
const FullKeyword = "full"

// AloneLabel marks the specs that run by themselves, after the main run.
const AloneLabel = "alone"

// sandbox is the environment lab test scope chooses for (just dev)
const sandbox = "sandbox"

// envLabels name where a spec may run; just test e2e adds the environment to every filter itself
var envLabels = map[string]bool{"sandbox": true, "testbed": true, "production": true}

// Config is test/scopes.yaml.
type Config struct {
	// Changes that run no sandbox spec
	Skip []string `yaml:"skip"`
	// Every label a spec or a filter may name
	Labels []string `yaml:"labels"`
	// Specs added to every non-empty selection (a label filter)
	Always string `yaml:"always"`
	// Labels of the specs that run alone, after the others, each in its own run, in this order
	Alone []string `yaml:"alone"`
	// The whole suite, alone specs included (a label filter)
	Full   string  `yaml:"full"`
	Groups []Group `yaml:"groups"`
	// Globs of what the map must cover (a trailing slash names directories)
	Components []string `yaml:"components"`
}

// Group maps changed paths to specs: a label filter, "" (only Always) or FullKeyword.
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
	if len(c.Labels) == 0 {
		return Config{}, fmt.Errorf("%s: labels is empty", path)
	}
	globs := append(append([]string{}, c.Skip...), c.Components...)
	filters := append([]string{c.Always, c.Full}, c.Alone...)
	for i, g := range c.Groups {
		if len(g.Paths) == 0 {
			return Config{}, fmt.Errorf("%s: group %d has no paths", path, i+1)
		}
		globs = append(globs, g.Paths...)
		if g.Tests != FullKeyword {
			filters = append(filters, g.Tests)
		}
	}
	for _, glob := range globs {
		if !doublestar.ValidatePattern(strings.TrimSuffix(glob, "/")) {
			return Config{}, fmt.Errorf("%s: invalid glob %q", path, glob)
		}
	}
	for _, f := range filters {
		if f == "" {
			continue
		}
		if _, err := types.ParseLabelFilter(f); err != nil {
			return Config{}, fmt.Errorf("%s: invalid label filter %q: %w", path, f, err)
		}
	}
	return c, nil
}

// Selection is what a change runs: the whole suite (Full), or Terms plus Always when Any file was not skipped.
// A Manual selection (just dev --tests ...) runs exactly its Terms.
type Selection struct {
	Full, Any, Manual bool
	// Label filters, ORed
	Terms []string
}

// FileFilters gives the label filters a changed test file selects (none for a deleted spec file).
type FileFilters func(file string) ([]string, error)

// Select maps changed files (relative to the repository root) to a Selection: skipped files add nothing; a file in
// groups adds their filters; a test file in no group adds the filters of the specs it can affect; any other file selects
// the whole suite.
func (c Config) Select(files []string, filters FileFilters) (Selection, error) {
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
				s.Terms = append(s.Terms, splitTerms(g.Tests)...)
			}
		}
		if matched {
			continue
		}
		if isTestFile(file) {
			selected, err := filters(file)
			if err != nil {
				return Selection{}, err
			}
			s.Terms = append(s.Terms, selected...)
			continue
		}
		s.Full = true
	}
	return s, nil
}

// ManualSelection runs exactly the given label filter.
func ManualSelection(filter string) Selection {
	return Selection{Any: true, Manual: true, Terms: splitTerms(filter)}
}

// Filter returns the selection's main run, (<its filters ORed>) && !alone, or "" when that selects no sandbox spec, and
// the alone runs: in alone:'s order, each alone label whose specs the selection reaches. just test e2e adds the
// environment to each.
func (c Config) Filter(s Selection, specs []Spec) (main string, alone []string) {
	var terms []string
	switch {
	case s.Full:
		terms = splitTerms(c.Full)
	case s.Manual:
		terms = s.Terms
	case s.Any:
		terms = append(append([]string{}, s.Terms...), splitTerms(c.Always)...)
	}
	union := joinTerms(terms)
	if union == "" {
		return "", nil
	}
	selected := types.MustParseLabelFilter(union) // Load and CheckManual validated the terms
	for _, spec := range specs {
		if slices.Contains(spec.Labels, sandbox) && !slices.Contains(spec.Labels, AloneLabel) && selected(spec.Labels) {
			main = enclose(union) + " && !" + AloneLabel
			break
		}
	}
	for _, label := range c.Alone {
		for _, spec := range specs {
			if slices.Contains(spec.Labels, sandbox) && slices.Contains(spec.Labels, AloneLabel) &&
				slices.Contains(spec.Labels, label) && selected(spec.Labels) {
				alone = append(alone, label)
				break
			}
		}
	}
	return main, alone
}

// CheckManual refuses a just dev --tests ... filter that does not parse, names a label outside labels: or selects no
// sandbox spec
func (c Config) CheckManual(filter string, specs []Spec) error {
	parsed, err := types.ParseLabelFilter(filter)
	if err != nil {
		return fmt.Errorf("tests: invalid label filter %q: %w", filter, err)
	}
	for _, label := range filterLabels(filter) {
		if !slices.Contains(c.Labels, label) {
			return fmt.Errorf("tests: %q is not a label (test/scopes.yaml labels:)", label)
		}
	}
	for _, spec := range specs {
		if slices.Contains(spec.Labels, sandbox) && parsed(spec.Labels) {
			return nil
		}
	}
	return fmt.Errorf("tests: %q selects no sandbox spec", filter)
}

// splitTerms splits a filter on its top-level ||, keeping parenthesised expressions whole
func splitTerms(filter string) []string {
	var terms []string
	depth, start := 0, 0
	for i := 0; i < len(filter); i++ {
		switch filter[i] {
		case '(':
			depth++
		case ')':
			depth--
		case '|':
			if depth == 0 && i+1 < len(filter) && filter[i+1] == '|' {
				terms = append(terms, filter[start:i])
				start = i + 2
				i++
			}
		}
	}
	terms = append(terms, filter[start:])
	var out []string
	for _, term := range terms {
		if term = strings.TrimSpace(term); term != "" {
			out = append(out, term)
		}
	}
	return out
}

// joinTerms ORs the distinct terms, sorted, each compound one in parentheses
func joinTerms(terms []string) string {
	seen := map[string]bool{}
	var out []string
	for _, term := range terms {
		if term = enclose(strings.TrimSpace(term)); term != "" && !seen[term] {
			seen[term] = true
			out = append(out, term)
		}
	}
	sort.Strings(out)
	return strings.Join(out, " || ")
}

// enclose puts a filter with a top-level operator in parentheses
func enclose(filter string) string {
	if strings.IndexAny(filter, "&|!,") < 0 && !strings.Contains(filter, " ") {
		return filter
	}
	if strings.HasPrefix(filter, "(") && strings.HasSuffix(filter, ")") {
		depth := 0
		for i := 0; i < len(filter); i++ {
			switch filter[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth == 0 && i < len(filter)-1 {
				return "(" + filter + ")"
			}
		}
		return filter
	}
	return "(" + filter + ")"
}

// filterLabels lists the labels a filter names
func filterLabels(filter string) []string {
	return regexp.MustCompile(`[^\s&|!(),]+`).FindAllString(filter, -1)
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

// isTestFile: a spec file, or a test/internal package's source, whose importers it selects
func isTestFile(file string) bool {
	ok, _ := doublestar.Match("test/e2e/*_test.go", file)
	return ok || isInternalFile(file)
}

func isInternalFile(file string) bool {
	ok, _ := doublestar.Match("test/internal/**/*.go", file)
	return ok && !strings.HasSuffix(file, "_test.go")
}
