// Package catalog generates Backstage's catalog (catalog/) and homepage's list of services (apps/homepage.yaml) from
// the one description each app and component keeps in its own files: a top-level homelab: list in apps/<name>.yaml
// (one item per route), homelab/* annotations in system/*/Chart.yaml and platform/*/Chart.yaml
// (docs/superpowers/specs/2026-11-16-backstage-design.md).
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Groups is homepage's order of its groups; a description naming another group is refused
var Groups = []string{"Popular", "Management", "Development", "Utilities"}

const (
	owner        = "group:default/homelab-admins"
	homepageFile = "apps/homepage.yaml"
)

// entry is one described thing: an app's route or a component
type entry struct {
	Name, Title, Description, Group, Icon, Host, Namespace, App, System, Type string
}

type appItem struct {
	Route       string `yaml:"route"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Group       string `yaml:"group"`
	Icon        string `yaml:"icon"`
}

type appFile struct {
	Route   map[string]struct{ Hostnames []string } `yaml:"route"`
	Homelab []appItem                               `yaml:"homelab"`
}

// Generate returns every generated file by its path under root
func Generate(root string) (map[string][]byte, error) {
	apps, err := readApps(root)
	if err != nil {
		return nil, err
	}
	var entries []entry
	entries = append(entries, apps...)
	for _, layer := range []string{"platform", "system"} {
		components, err := readCharts(root, layer)
		if err != nil {
			return nil, err
		}
		entries = append(entries, components...)
	}
	for _, e := range entries {
		if e.Group != "" && !slices.Contains(Groups, e.Group) {
			return nil, fmt.Errorf("%s: group %q is not one of homepage's (%s)", e.Name, e.Group, strings.Join(Groups, ", "))
		}
		if e.Group != "" && (e.Host == "" || e.Icon == "") {
			return nil, fmt.Errorf("%s: a homepage entry (group %q) needs a host and an icon", e.Name, e.Group)
		}
	}

	files := map[string][]byte{"catalog-info.yaml": entryPoint()}
	for _, system := range []struct{ file, name string }{{"apps", "homelab-apps"}, {"platform", "homelab-platform"}, {"system", "homelab-system"}} {
		var mine []entry
		for _, e := range entries {
			if e.System == system.name {
				mine = append(mine, e)
			}
		}
		files["catalog/"+system.file+".yaml"] = components(mine)
	}
	files["catalog/systems.yaml"] = systems()

	page, err := os.ReadFile(filepath.Join(root, homepageFile))
	if err != nil {
		return nil, err
	}
	page, err = replaceServices(page, services(entries))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", homepageFile, err)
	}
	files[homepageFile] = page
	return files, nil
}

// Write generates and writes every file
func Write(root string) error {
	files, err := Generate(root)
	if err != nil {
		return err
	}
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Check fails when a generated file differs from the one on disk
func Check(root string) error {
	files, err := Generate(root)
	if err != nil {
		return err
	}
	var stale []string
	for path, content := range files {
		have, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || !bytes.Equal(have, content) {
			stale = append(stale, path)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	slices.Sort(stale)
	return fmt.Errorf("out of date (run lab catalog generate): %s", strings.Join(stale, ", "))
}

func readApps(root string) ([]entry, error) {
	paths, err := filepath.Glob(filepath.Join(root, "apps", "*.yaml"))
	if err != nil {
		return nil, err
	}
	slices.Sort(paths)
	var entries []entry
	for _, path := range paths {
		rel, _ := filepath.Rel(root, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var f appFile
		if err := yaml.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		if len(f.Homelab) == 0 {
			return nil, fmt.Errorf("%s: no homelab: description", rel)
		}
		app := strings.TrimSuffix(filepath.Base(path), ".yaml")
		for _, item := range f.Homelab {
			if item.Name == "" || item.Description == "" {
				return nil, fmt.Errorf("%s: an item without a name or description", rel)
			}
			name := app
			if item.Route != "" && item.Route != "main" {
				name = app + "-" + item.Route
			}
			host := ""
			if item.Route != "" {
				route, ok := f.Route[item.Route]
				if !ok || len(route.Hostnames) == 0 {
					return nil, fmt.Errorf("%s: route %q has no hostnames", rel, item.Route)
				}
				host, _, _ = strings.Cut(route.Hostnames[0], ".")
			}
			entries = append(entries, entry{Name: name, Title: item.Name, Description: item.Description, Group: item.Group,
				Icon: item.Icon, Host: host, Namespace: app, App: app, System: "homelab-apps", Type: "service"})
		}
	}
	return entries, nil
}

func readCharts(root, layer string) ([]entry, error) {
	paths, err := filepath.Glob(filepath.Join(root, layer, "*", "Chart.yaml"))
	if err != nil {
		return nil, err
	}
	slices.Sort(paths)
	var entries []entry
	for _, path := range paths {
		rel, _ := filepath.Rel(root, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var chart struct{ Annotations map[string]string }
		if err := yaml.Unmarshal(data, &chart); err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		a := chart.Annotations
		if a["homelab/name"] == "" || a["homelab/description"] == "" {
			return nil, fmt.Errorf("%s: no homelab/name or homelab/description annotation", rel)
		}
		dir := filepath.Base(filepath.Dir(path))
		entries = append(entries, entry{Name: dir, Title: a["homelab/name"], Description: a["homelab/description"],
			Group: a["homelab/group"], Icon: a["homelab/icon"], Host: a["homelab/host"], Namespace: dir, App: dir,
			System: "homelab-" + layer, Type: "platform"})
	}
	return entries, nil
}

const header = "# Generated by lab catalog generate from each app's and component's description: do not edit\n"

// entryPoint is the repository's catalog-info.yaml: the Domain homelab, whose docs are this repository's MkDocs site
// (TechDocs builds mkdocs.yml from the entity's own directory), and the Location of the generated files
func entryPoint() []byte {
	var b strings.Builder
	b.WriteString(header)
	writeDocs(&b, []map[string]any{{
		"apiVersion": "backstage.io/v1alpha1", "kind": "Domain",
		"metadata": map[string]any{"name": "homelab", "description": "The homelab",
			"annotations": map[string]any{"backstage.io/techdocs-ref": "dir:."}},
		"spec": map[string]any{"owner": owner},
	}, {
		"apiVersion": "backstage.io/v1alpha1", "kind": "Location",
		"metadata": map[string]any{"name": "homelab"},
		"spec": map[string]any{"targets": []string{"./catalog/systems.yaml", "./catalog/apps.yaml",
			"./catalog/platform.yaml", "./catalog/system.yaml"}},
	}})
	return []byte(b.String())
}

func systems() []byte {
	var b strings.Builder
	b.WriteString(header)
	var docs []map[string]any
	for _, s := range []struct{ name, description string }{
		{"homelab-apps", "The apps people use (apps/)"},
		{"homelab-platform", "What the apps build on: identity, git, CI, secrets, data (platform/)"},
		{"homelab-system", "The cluster's own services: networking, policy, observability, backups (system/)"},
	} {
		docs = append(docs, map[string]any{
			"apiVersion": "backstage.io/v1alpha1", "kind": "System",
			"metadata": map[string]any{"name": s.name, "description": s.description},
			"spec":     map[string]any{"owner": owner, "domain": "homelab"},
		})
	}
	writeDocs(&b, docs)
	return []byte(b.String())
}

func components(entries []entry) []byte {
	var b strings.Builder
	b.WriteString(header)
	var docs []map[string]any
	for _, e := range entries {
		annotations := map[string]any{
			"argocd/app-name":                        e.App,
			"backstage.io/kubernetes-namespace":      e.Namespace,
			"backstage.io/kubernetes-label-selector": "app.kubernetes.io/instance=" + e.App,
		}
		if e.Host != "" {
			// The host's first label: Backstage adds the environment's domain (no domain in git)
			annotations["homelab/host"] = e.Host
		}
		docs = append(docs, map[string]any{
			"apiVersion": "backstage.io/v1alpha1", "kind": "Component",
			"metadata": map[string]any{"name": e.Name, "title": e.Title, "description": e.Description, "annotations": annotations},
			"spec":     map[string]any{"type": e.Type, "lifecycle": "production", "owner": owner, "system": e.System},
		})
	}
	writeDocs(&b, docs)
	return []byte(b.String())
}

func writeDocs(b *strings.Builder, docs []map[string]any) {
	for i, doc := range docs {
		if i > 0 {
			b.WriteString("---\n")
		}
		var out bytes.Buffer
		enc := yaml.NewEncoder(&out)
		enc.SetIndent(2)
		_ = enc.Encode(doc)
		b.Write(out.Bytes())
	}
}

// services is homepage's services.yaml: the described hosts by group, in Groups' order, each group by name
func services(entries []entry) string {
	var b strings.Builder
	b.WriteString("# Generated by lab catalog generate from each app's and component's description: do not edit\n")
	for _, group := range Groups {
		var mine []entry
		for _, e := range entries {
			if e.Group == group {
				mine = append(mine, e)
			}
		}
		if len(mine) == 0 {
			continue
		}
		slices.SortFunc(mine, func(a, b entry) int { return strings.Compare(a.Title, b.Title) })
		fmt.Fprintf(&b, "- %s:\n", group)
		for _, e := range mine {
			fmt.Fprintf(&b, "  - %s:\n      href: https://%s.{{ .Values.global.baseDomain }}\n      description: %s\n      icon: %s\n",
				scalar(e.Title), e.Host, scalar(e.Description), scalar(e.Icon))
		}
	}
	return b.String()
}

// replaceServices replaces the services.yaml block scalar of homepage's app file, keeping every other line
func replaceServices(page []byte, content string) ([]byte, error) {
	lines := strings.SplitAfter(string(page), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "services.yaml: |" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, errors.New("no services.yaml: | block")
	}
	keyIndent := len(lines[start]) - len(strings.TrimLeft(lines[start], " "))
	end := start + 1
	for end < len(lines) {
		line := lines[end]
		if strings.TrimSpace(line) != "" && len(line)-len(strings.TrimLeft(line, " ")) <= keyIndent {
			break
		}
		end++
	}
	indent := strings.Repeat(" ", keyIndent+2)
	var block strings.Builder
	for _, line := range strings.SplitAfter(strings.TrimSuffix(content, "\n"), "\n") {
		block.WriteString(indent + strings.TrimSuffix(line, "\n") + "\n")
	}
	out := strings.Join(lines[:start+1], "") + block.String() + strings.Join(lines[end:], "")
	return []byte(out), nil
}

// scalar is s as a YAML scalar, quoted only when it must be
func scalar(s string) string {
	out, _ := yaml.Marshal(s)
	return strings.TrimSuffix(string(out), "\n")
}
