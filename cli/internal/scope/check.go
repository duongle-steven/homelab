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
