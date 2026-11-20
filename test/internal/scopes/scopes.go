// Package scopes reads test/scopes.yaml's core and deploy groups: which group of the sandbox's components a spec's
// labels or an app's namespace need (docs/superpowers/specs/2026-11-20-sandbox-groups-design.md)
package scopes

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"gopkg.in/yaml.v3"
)

// Map is test/scopes.yaml's core and deployGroups
type Map struct {
	Core struct {
		Components []string `yaml:"components"`
		Labels     []string `yaml:"labels"`
	} `yaml:"core"`
	DeployGroups map[string]struct {
		Components []string `yaml:"components"`
		Needs      []string `yaml:"needs"`
		Labels     []string `yaml:"labels"`
	} `yaml:"deployGroups"`
}

// Load reads test/scopes.yaml
func Load() (Map, error) {
	raw, err := os.ReadFile(filepath.Join(repo.Root(), "test", "scopes.yaml"))
	if err != nil {
		return Map{}, err
	}
	var m Map
	return m, yaml.Unmarshal(raw, &m)
}

// GroupsOfLabels returns the sorted groups that serve any of labels: what a spec carrying them needs deployed
func (m Map) GroupsOfLabels(labels []string) []string {
	var groups []string
	for name, g := range m.DeployGroups {
		for _, l := range labels {
			if slices.Contains(g.Labels, l) {
				groups = append(groups, name)
				break
			}
		}
	}
	sort.Strings(groups)
	return groups
}

// GroupOfNamespace returns the group of the component or app deployed in namespace ns, "" for the core or none
func (m Map) GroupOfNamespace(ns string) string {
	for name, g := range m.DeployGroups {
		for _, c := range g.Components {
			if strings.TrimSuffix(path.Base(c), ".yaml") == ns {
				return name
			}
		}
	}
	return ""
}
