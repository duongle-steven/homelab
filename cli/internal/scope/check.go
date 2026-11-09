package scope

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/onsi/ginkgo/v2/types"
)

// Check lists what the map misses: components with a tracked file that no group or skip knows (it would run the
// whole suite), labels outside labels: (in a filter or on a spec) or on no spec, filters that select no spec, and alone
// labels or alone specs that do not meet.
func (c Config) Check(tracked []string, specs []Spec) []string {
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

	used := map[string]bool{}
	for _, spec := range specs {
		for _, label := range spec.Labels {
			used[label] = true
			if !slices.Contains(c.Labels, label) {
				problems = append(problems, fmt.Sprintf("%s (%s): label %q is not in labels:", spec.Name, spec.File, label))
			}
		}
		if slices.Contains(spec.Labels, AloneLabel) && !slices.ContainsFunc(c.Alone, func(a string) bool { return slices.Contains(spec.Labels, a) }) {
			problems = append(problems, fmt.Sprintf("%s (%s): labelled alone, but alone: lists none of its labels", spec.Name, spec.File))
		}
	}
	for _, label := range c.Labels {
		if !used[label] {
			problems = append(problems, fmt.Sprintf("labels: %q labels no spec", label))
		}
	}

	check := func(where, filter string) {
		unknown := false
		for _, label := range filterLabels(filter) {
			if !slices.Contains(c.Labels, label) {
				problems = append(problems, fmt.Sprintf("%s: %q is not in labels:", where, label))
				unknown = true
			}
		}
		if unknown {
			return
		}
		selected := types.MustParseLabelFilter(filter) // Load validated it
		if !slices.ContainsFunc(specs, func(s Spec) bool { return selected(s.Labels) }) {
			problems = append(problems, fmt.Sprintf("%s: %q selects no spec", where, filter))
		}
	}
	if c.Always != "" {
		check("always", c.Always)
	}
	check("full", c.Full)
	for _, label := range c.Alone {
		check("alone", label)
		if !slices.ContainsFunc(specs, func(s Spec) bool {
			return slices.Contains(s.Labels, label) && slices.Contains(s.Labels, AloneLabel)
		}) {
			problems = append(problems, fmt.Sprintf("alone: %q labels no spec labelled alone", label))
		}
	}
	for i, g := range c.Groups {
		if g.Tests == FullKeyword || g.Tests == "" {
			continue
		}
		check(fmt.Sprintf("group %d", i+1), g.Tests)
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
