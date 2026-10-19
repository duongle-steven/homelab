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
