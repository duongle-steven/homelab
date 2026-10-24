package rotate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
		if attempt < 2 {
			time.Sleep(5 * time.Second)
		}
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
	problems = append(problems, checkReferences(root, entries)...)
	sort.Strings(problems)
	return problems
}

var (
	secretName = regexp.MustCompile(`(?m)^\s*(?:-\s*)?(?:name|forceRename|existingSecret):\s*["']?([A-Za-z0-9._-]+)["']?\s*$`)
	tofuRes    = regexp.MustCompile(`(?m)^resource\s+"([^"]+)"\s+"([^"]+)"`)
)

// checkReferences is the other direction: what an entry names must exist. OpenTofu entries name a resource under
// infra/_modules, readers name ExternalSecrets in system, platform or apps. An entry with Pending names something a
// later task creates and is skipped.
func checkReferences(root string, entries []Entry) []string {
	resources, names := map[string]bool{}, map[string]bool{}
	walk := func(dir string, exts []string, f func(path string, data []byte)) {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !slices.Contains(exts, filepath.Ext(path)) {
				return nil
			}
			if data, err := os.ReadFile(path); err == nil {
				f(path, data)
			}
			return nil
		})
	}
	walk("infra/_modules", []string{".tf"}, func(_ string, data []byte) {
		for _, m := range tofuRes.FindAllSubmatch(data, -1) {
			resources[string(m[1])+"."+string(m[2])] = true
		}
	})
	for _, dir := range []string{"system", "platform", "apps"} {
		walk(dir, []string{".yaml", ".tpl"}, func(_ string, data []byte) {
			for _, m := range secretName.FindAllSubmatch(data, -1) {
				names[string(m[1])] = true
			}
		})
	}
	var problems []string
	for _, e := range entries {
		if e.Pending != "" {
			continue
		}
		if r := e.Settings["resource"]; e.Kind == KindOpenTofu && r != "" && !resources[r] {
			problems = append(problems, fmt.Sprintf("%s: resource %s is not under infra/_modules", e.Name, r))
		}
		for _, rd := range e.Readers {
			for _, es := range rd.ExternalSecrets {
				if !names[es] {
					problems = append(problems, fmt.Sprintf("%s: ExternalSecret %s is not in system, platform or apps", e.Name, es))
				}
			}
		}
	}
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

// Scheduled reports whether the CronJob rotates e (lab secrets rotate --scheduled): an entry with a period that runs
// in the cluster, never a manual or OpenTofu one.
func Scheduled(e Entry) bool {
	return e.Period > 0 && !slices.Contains([]Kind{KindManual, KindOpenTofu, KindNever, KindAutomatic, KindScript}, e.Kind)
}

// CheckScheduled compares what the scheduled rotation may touch with what its entries need: the readers in
// platform/secrets-rotation/values.yaml (its RBAC: namespaces, ExternalSecrets, Deployments, StatefulSets) and the
// paths in platform/openbao/values.yaml (policy rotator).
func CheckScheduled(root string, entries []Entry) []string {
	var problems []string
	raw, err := os.ReadFile(filepath.Join(root, "platform/secrets-rotation/values.yaml"))
	if err != nil {
		return []string{fmt.Sprintf("platform/secrets-rotation/values.yaml: %v", err)}
	}
	type access struct {
		ExternalSecrets []string `yaml:"externalSecrets"`
		Deployments     []string `yaml:"deployments"`
		StatefulSets    []string `yaml:"statefulsets"`
	}
	var v struct {
		Readers map[string]access `yaml:"readers"`
	}
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return []string{fmt.Sprintf("platform/secrets-rotation/values.yaml: %v", err)}
	}
	policy, err := os.ReadFile(filepath.Join(root, "platform/openbao/values.yaml"))
	if err != nil {
		return []string{fmt.Sprintf("platform/openbao/values.yaml: %v", err)}
	}
	// What the scheduled entries need, by namespace and kind: "ExternalSecret", "Deployment", "StatefulSet"
	needed := map[string]map[string][]string{}
	for _, e := range entries {
		if !Scheduled(e) || e.Pending != "" {
			continue
		}
		for _, r := range namespaced(e) {
			if needed[r.Namespace] == nil {
				needed[r.Namespace] = map[string][]string{}
			}
			if _, ok := v.Readers[r.Namespace]; !ok {
				problems = append(problems, fmt.Sprintf("%s: %s's readers are there, not in platform/secrets-rotation's readers", r.Namespace, e.Name))
			}
			needed[r.Namespace]["ExternalSecret"] = append(needed[r.Namespace]["ExternalSecret"], r.ExternalSecrets...)
			for _, w := range r.Workloads {
				needed[r.Namespace][w.Kind] = append(needed[r.Namespace][w.Kind], w.Name)
			}
		}
		for _, p := range e.Paths {
			if !strings.Contains(string(policy), `secret/data/`+p+`\"`) {
				problems = append(problems, fmt.Sprintf("%s: %s's path is not in OpenBao's policy rotator (platform/openbao/values.yaml)", p, e.Name))
			}
		}
	}
	for ns, got := range v.Readers {
		if needed[ns] == nil {
			problems = append(problems, fmt.Sprintf("%s: no scheduled entry reads there, yet platform/secrets-rotation may touch it", ns))
			continue
		}
		for kind, allowed := range map[string][]string{"ExternalSecret": got.ExternalSecrets, "Deployment": got.Deployments, "StatefulSet": got.StatefulSets} {
			want := needed[ns][kind]
			for _, name := range allowed {
				if !slices.Contains(want, name) {
					problems = append(problems, fmt.Sprintf("%s: %s %s is allowed but no scheduled entry needs it", ns, kind, name))
				}
			}
			for _, name := range want {
				if !slices.Contains(allowed, name) {
					problems = append(problems, fmt.Sprintf("%s: %s %s is needed by a scheduled entry but not allowed", ns, kind, name))
				}
			}
		}
	}
	slices.Sort(problems)
	return slices.Compact(problems)
}
