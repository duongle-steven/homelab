package rotate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/forgejo"
	"go.yaml.in/yaml/v3"
)

// render renders a chart or an app file with the production values, the way Argo CD does (a test replaces it).
var render = func(root, target string) ([]byte, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var out []byte
	// Chart downloads sometimes reset the connection: try three times
	for attempt := 0; attempt < 3; attempt++ {
		cmd := exec.Command(filepath.Join(root, "scripts", "render-chart"), target)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if out, err = cmd.Output(); err == nil {
			return out, nil
		}
		err = fmt.Errorf("render %s: %w: %s", target, err, strings.TrimSpace(stderr.String()))
		time.Sleep(5 * time.Second)
	}
	return nil, err
}

// Check finds every secret the repository creates and reports each one the catalog lacks: the generated secrets,
// the infra-secrets paths, the confidential Kanidm clients and the Forgejo tokens. It reads root, a checkout.
func Check(root string, entries []Entry) []string {
	var problems []string
	names := map[string]bool{}
	paths := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
		for _, p := range e.Paths {
			paths[p] = true
		}
	}
	missingName := func(name, source string) {
		if !names[name] {
			problems = append(problems, fmt.Sprintf("%s: no entry in cli/internal/rotate/catalog.yaml (%s)", name, source))
		}
	}
	missingPath := func(path, source string) {
		if !paths[path] {
			problems = append(problems, fmt.Sprintf("%s: no entry has this path in cli/internal/rotate/catalog.yaml (%s)", path, source))
		}
	}

	generated, err := valuesFiles(root, "platform/generated-secrets")
	problems = append(problems, errs(err)...)
	for _, v := range generated {
		for _, list := range []string{"secrets", "extraSecrets"} {
			for _, s := range v.list(list) {
				if name, _ := s["name"].(string); name != "" {
					missingName(name, "platform/generated-secrets "+v.file)
				}
			}
		}
	}
	infra, err := valuesFiles(root, "platform/infra-secrets")
	problems = append(problems, errs(err)...)
	for _, v := range infra {
		for _, s := range v.list("secrets") {
			if path, _ := s["path"].(string); path != "" {
				missingPath(path, "platform/infra-secrets "+v.file)
			}
		}
	}
	for _, p := range []string{forgejo.DeployPath, forgejo.ArgoCDPath, forgejo.WoodpeckerPath} {
		missingPath(p, "cli/internal/forgejo/sync.go")
	}

	clients, err := confidentialClients(root)
	problems = append(problems, errs(err)...)
	for _, c := range clients {
		missingName("kanidm-client."+c.name, c.target)
	}
	sort.Strings(problems)
	return problems
}

func errs(err error) []string {
	if err == nil {
		return nil
	}
	return []string{err.Error()}
}

type values struct {
	file string
	doc  map[string]any
}

func (v values) list(key string) []map[string]any {
	var out []map[string]any
	items, _ := v.doc[key].([]any)
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// valuesFiles reads values.yaml and values-*.yaml of a component.
func valuesFiles(root, dir string) ([]values, error) {
	files, err := filepath.Glob(filepath.Join(root, dir, "values*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s: no values files under %s", dir, root)
	}
	var out []values
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, values{file: filepath.Base(f), doc: doc})
	}
	return out, nil
}

type client struct{ name, target string }

// confidentialClients renders only the charts and app files that hold a KanidmOAuth2Client and returns the clients
// whose public is not true.
func confidentialClients(root string) ([]client, error) {
	targets, err := clientTargets(root)
	if err != nil {
		return nil, err
	}
	var out []client
	for _, target := range targets {
		rendered, err := render(root, target)
		if err != nil {
			return nil, err
		}
		dec := yaml.NewDecoder(bytes.NewReader(rendered))
		for {
			var doc struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
				Spec struct {
					Public bool `yaml:"public"`
				} `yaml:"spec"`
			}
			if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, fmt.Errorf("render %s: %w", target, err)
			}
			if doc.Kind == "KanidmOAuth2Client" && !doc.Spec.Public {
				out = append(out, client{name: doc.Metadata.Name, target: target})
			}
		}
	}
	return out, nil
}

// clientTargets are the chart directories (system, platform, apps) whose templates, and the app files that, name the
// kind KanidmOAuth2Client.
func clientTargets(root string) ([]string, error) {
	var targets []string
	has := func(path string) (bool, error) {
		data, err := os.ReadFile(path)
		return bytes.Contains(data, []byte("kind: KanidmOAuth2Client")), err
	}
	apps, err := filepath.Glob(filepath.Join(root, "apps", "*.yaml"))
	if err != nil {
		return nil, err
	}
	for _, f := range apps {
		if ok, err := has(f); err != nil {
			return nil, err
		} else if ok {
			targets = append(targets, filepath.ToSlash(filepath.Join("apps", filepath.Base(f))))
		}
	}
	charts, err := filepath.Glob(filepath.Join(root, "*", "*", "Chart.yaml"))
	if err != nil {
		return nil, err
	}
	for _, c := range charts {
		dir := filepath.Dir(c)
		templates, err := filepath.Glob(filepath.Join(dir, "templates", "*.yaml"))
		if err != nil {
			return nil, err
		}
		for _, f := range templates {
			ok, err := has(f)
			if err != nil {
				return nil, err
			}
			if ok {
				rel, err := filepath.Rel(root, dir)
				if err != nil {
					return nil, err
				}
				targets = append(targets, filepath.ToSlash(rel))
				break
			}
		}
	}
	slices.Sort(targets)
	return targets, nil
}
