package scope

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/onsi/ginkgo/v2/types"
)

// Core is what every sandbox runs: its components, and the labels whose specs need nothing more
// (docs/superpowers/specs/2026-11-20-sandbox-groups-design.md).
type Core struct {
	Components []string `yaml:"components"`
	Labels     []string `yaml:"labels"`
}

// DeployGroup is a set of the sandbox's components deployed together: when a selected spec carries one of Labels, when
// a branch changes one of Components, or when another deployed group Needs it.
type DeployGroup struct {
	Components []string `yaml:"components"`
	Needs      []string `yaml:"needs"`
	Labels     []string `yaml:"labels"`
}

// GroupsFor returns the sorted groups a run deploys: those serving a label of any sandbox spec filter selects ("" selects
// none), those holding a changed file, and their needs.
func (c Config) GroupsFor(filter string, specs []Spec, changed []string) []string {
	wanted := map[string]bool{}
	if filter != "" {
		selected := types.MustParseLabelFilter(filter)
		labels := map[string]bool{}
		for _, spec := range specs {
			if slices.Contains(spec.Labels, sandbox) && selected(spec.Labels) {
				for _, l := range spec.Labels {
					labels[l] = true
				}
			}
		}
		for name, g := range c.DeployGroups {
			for _, l := range g.Labels {
				if labels[l] {
					wanted[name] = true
				}
			}
		}
	}
	for _, file := range changed {
		for name, g := range c.DeployGroups {
			for _, component := range g.Components {
				if file == component || strings.HasPrefix(file, component+"/") {
					wanted[name] = true
				}
			}
		}
	}
	return c.WithNeeds(keys(wanted))
}

// WithNeeds returns groups and every group they need, sorted; an unknown name is an error's to report (Unknown).
func (c Config) WithNeeds(groups []string) []string {
	wanted := map[string]bool{}
	for _, g := range groups {
		wanted[g] = true
	}
	for queue := keys(wanted); len(queue) > 0; queue = queue[1:] {
		for _, need := range c.DeployGroups[queue[0]].Needs {
			if !wanted[need] {
				wanted[need] = true
				queue = append(queue, need)
			}
		}
	}
	return keys(wanted)
}

// AllGroups returns every group, sorted (--full, the release sandbox).
func (c Config) AllGroups() []string {
	all := map[string]bool{}
	for name := range c.DeployGroups {
		all[name] = true
	}
	return keys(all)
}

// GroupOf returns the group holding component (a directory such as system/loki, or an app file), "" for the core or none.
func (c Config) GroupOf(component string) string {
	for name, g := range c.DeployGroups {
		if slices.Contains(g.Components, component) {
			return name
		}
	}
	return ""
}

// CheckGroups lists what the groups get wrong: a component in two groups (or a group and the core), a need naming no
// group, a label outside labels:, and a label on a sandbox spec that neither the core nor a group serves.
func (c Config) CheckGroups(specs []Spec) []string {
	var problems []string
	owner := map[string]string{}
	for _, component := range c.Core.Components {
		owner[component] = "core"
	}
	known := map[string]bool{}
	for _, l := range c.Labels {
		known[l] = true
	}
	served := map[string]bool{}
	for _, l := range c.Core.Labels {
		served[l] = true
	}
	for _, name := range c.AllGroups() {
		g := c.DeployGroups[name]
		for _, component := range g.Components {
			if !doublestar.ValidatePattern(component) {
				problems = append(problems, fmt.Sprintf("%s: component %q is not a path", name, component))
			}
			if other, ok := owner[component]; ok {
				problems = append(problems, fmt.Sprintf("%s: in groups %s and %s", component, other, name))
			}
			owner[component] = name
		}
		for _, need := range g.Needs {
			if _, ok := c.DeployGroups[need]; !ok {
				problems = append(problems, fmt.Sprintf("%s: needs %s, which is no group", name, need))
			}
		}
		for _, l := range g.Labels {
			if !known[l] {
				problems = append(problems, fmt.Sprintf("%s: label %s is not in labels", name, l))
			}
			served[l] = true
		}
	}
	unserved := map[string]bool{}
	for _, spec := range specs {
		if !slices.Contains(spec.Labels, sandbox) {
			continue
		}
		for _, l := range spec.Labels {
			if known[l] && !envLabels[l] && l != AloneLabel && !served[l] {
				unserved[l] = true
			}
		}
	}
	for _, l := range keys(unserved) {
		problems = append(problems, fmt.Sprintf("%s: served by no group nor the core (test/scopes.yaml deployGroups)", l))
	}
	sort.Strings(problems)
	return problems
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
