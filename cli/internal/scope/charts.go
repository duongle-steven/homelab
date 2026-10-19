package scope

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// chartRoots hold wrapper charts (a directory with Chart.yaml) and, under apps/, app files
var chartRoots = []string{"system", "platform", "apps"}

// Changes to these render every chart: they feed every render (scripts/render-chart, the values it passes, the apps
// ApplicationSet's app-template pin in system/argocd)
var renderAll = []string{"scripts/render-chart", "scripts/env-values", "infra/*/cluster/config.json", "system/argocd/values*.yaml"}

// Charts lists the render targets (chart directories and apps/<name>.yaml) the changed files touch, or all of them;
// targets that no longer exist are left out.
func Charts(root string, files []string, all bool) ([]string, error) {
	targets := map[string]bool{}
	for _, f := range files {
		for _, pattern := range renderAll {
			if ok, _ := doublestar.Match(pattern, f); ok {
				all = true
			}
		}
	}
	if all {
		return allCharts(root)
	}
	for _, f := range files {
		if t := target(root, f); t != "" {
			targets[t] = true
		}
	}
	return sorted(targets), nil
}

// target is the chart directory or app file a path belongs to, "" when none exists
func target(root, f string) string {
	if path.Dir(f) == "apps" && strings.HasSuffix(f, ".yaml") {
		if exists(filepath.Join(root, f)) {
			return f
		}
		return ""
	}
	for dir := path.Dir(f); strings.Contains(dir, "/"); dir = path.Dir(dir) {
		if exists(filepath.Join(root, dir, "Chart.yaml")) {
			if top := strings.SplitN(dir, "/", 2)[0]; contains(chartRoots, top) {
				return dir
			}
			return ""
		}
	}
	return ""
}

func allCharts(root string) ([]string, error) {
	targets := map[string]bool{}
	for _, top := range chartRoots {
		charts, err := filepath.Glob(filepath.Join(root, top, "*", "Chart.yaml"))
		if err != nil {
			return nil, err
		}
		for _, c := range charts {
			rel, err := filepath.Rel(root, filepath.Dir(c))
			if err != nil {
				return nil, err
			}
			targets[filepath.ToSlash(rel)] = true
		}
	}
	apps, err := filepath.Glob(filepath.Join(root, "apps", "*.yaml"))
	if err != nil {
		return nil, err
	}
	for _, a := range apps {
		targets["apps/"+filepath.Base(a)] = true
	}
	return sorted(targets), nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

func sorted(set map[string]bool) []string {
	out := []string{}
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
