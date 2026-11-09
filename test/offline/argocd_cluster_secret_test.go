package offline

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clusterSecret runs cluster-secret.sh with env and returns its output
func clusterSecret(t *testing.T, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", repo.Root()+"/infra/_modules/argocd/cluster-secret.sh")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	return string(out), err
}

func yqOn(t *testing.T, doc, expr string) string {
	t.Helper()
	cmd := exec.Command("yq", "-o=json", "-I=0", expr)
	cmd.Stdin = strings.NewReader(doc)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

// The bootstrap's cluster Secret carries what the ApplicationSets read: every annotation always present, one label per
// optional app asked
func TestArgoCDClusterSecret(t *testing.T) {
	out, err := clusterSecret(t, "REPOSITORY=https://github.com/example/homelab", "BRANCH=sandbox", "OPTIONAL_APPS=paperless paperless")
	require.NoError(t, err)
	assert.Equal(t, `"Secret"`, yqOn(t, out, ".kind"))
	assert.Equal(t, `{"name":"in-cluster","namespace":"argocd"}`, yqOn(t, out, `.metadata | {"name": .name, "namespace": .namespace}`))
	assert.JSONEq(t, `{"argocd.argoproj.io/secret-type":"cluster","homelab/bootstrap":"true","homelab/app-paperless":"true"}`,
		yqOn(t, out, ".metadata.labels"))
	assert.JSONEq(t, `{"repository":"https://github.com/example/homelab","revision":"sandbox","backupS3Url":""}`,
		yqOn(t, out, ".metadata.annotations"))
	assert.JSONEq(t, `{"name":"in-cluster","server":"https://kubernetes.default.svc"}`, yqOn(t, out, ".stringData"))

	out, err = clusterSecret(t, "REPOSITORY=r", "BRANCH=sandbox", "REVISION=abc123", "BACKUP_S3_URL=https://s3.example")
	require.NoError(t, err)
	assert.Equal(t, `"abc123"`, yqOn(t, out, ".metadata.annotations.revision"))
	assert.Equal(t, `"https://s3.example"`, yqOn(t, out, ".metadata.annotations.backupS3Url"))
	assert.Equal(t, `[]`, yqOn(t, out, `[.metadata.labels | keys | .[] | select(test("^homelab/app-"))]`))

	_, err = clusterSecret(t, "BRANCH=sandbox")
	assert.Error(t, err, "no REPOSITORY")
	_, err = clusterSecret(t, "REPOSITORY=r")
	assert.Error(t, err, "no BRANCH")
}

// On a new cluster the ApplicationSets' generators match nothing until the Secret exists, and re-applying them before it
// with a changed selector would delete every Application: the bootstrap applies the Secret before anything else
func TestArgoCDBootstrapAppliesSecretFirst(t *testing.T) {
	dir, env := fakeBin(t, map[string]string{"kubectl": `case "$*" in
  *"create namespace"*) printf 'apiVersion: v1\nkind: Namespace\nmetadata: {name: argocd}\n' ;;
  *wait*) ;;
  *apply*) yq -N '.kind + "/" + .metadata.name' >> "$FIXTURES/applied" ;;
  *) exit 1 ;;
esac`})
	root, err := filepath.Abs(repo.Root())
	require.NoError(t, err)
	cmd := exec.Command("sh", root+"/infra/_modules/argocd/bootstrap.sh")
	cmd.Env = append(env, "CHART="+root+"/system/argocd", "VALUES=values-dev.yaml", "ENVIRONMENT=dev",
		"REPOSITORY=https://github.com/example/homelab", "BRANCH=sandbox", "KUBECONFIG=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	raw, err := os.ReadFile(filepath.Join(dir, "applied"))
	require.NoError(t, err)
	applied := strings.Fields(string(raw))
	secret := -1
	for i, object := range applied {
		switch {
		case object == "Secret/in-cluster" && secret < 0:
			secret = i
		case strings.HasPrefix(object, "CustomResourceDefinition/"), strings.HasPrefix(object, "ApplicationSet/"):
			require.GreaterOrEqual(t, secret, 0, "%s applied before Secret/in-cluster", object)
		}
	}
	require.GreaterOrEqual(t, secret, 0, "Secret/in-cluster never applied: %v", applied)
}

// renderAppSets renders system/argocd for env (scripts/render-chart's "" is production) and returns its ApplicationSets
// as JSON, one per line
func renderAppSets(t *testing.T, env string) map[string]map[string]any {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/argocd "$0" | yq -o=json -I=0 'select(.kind == "ApplicationSet")'`, env)
	cmd.Dir = repo.Root()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	sets := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var set map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &set), line)
		sets[set["metadata"].(map[string]any)["name"].(string)] = set
	}
	return sets
}

// walk calls visit on every map under v
func walk(v any, visit func(map[string]any)) {
	switch v := v.(type) {
	case map[string]any:
		visit(v)
		for _, inner := range v {
			walk(inner, visit)
		}
	case []any:
		for _, inner := range v {
			walk(inner, visit)
		}
	}
}

const appTemplateRepo = "https://bjw-s-labs.github.io/helm-charts"

// Every homelab source and generator, in every values file, reads the repository and the revision from the cluster
// Secret, which a clusters generator selects; app-template keeps its literal version
func TestArgoCDReadsClusterSecret(t *testing.T) {
	written, err := clusterSecret(t, "REPOSITORY=r", "BRANCH=b")
	require.NoError(t, err)
	var annotations map[string]any
	require.NoError(t, json.Unmarshal([]byte(yqOn(t, written, ".metadata.annotations")), &annotations))
	known := regexp.MustCompile(`\.metadata\.annotations\.([A-Za-z0-9_]+)`)
	for _, env := range []string{"", "dev", "testbed", "seed"} {
		sets := renderAppSets(t, env)
		require.Contains(t, sets, "root", "env %q", env)
		for name, set := range sets {
			spec := set["spec"].(map[string]any)
			for i, g := range asList(spec["generators"]) {
				assert.Equal(t, "true", dig(g, "matrix", "generators", 0, "clusters", "selector", "matchLabels", "homelab/bootstrap"),
					"env %q %s generator %d", env, name, i)
			}
			homelab := 0
			walk(spec, func(m map[string]any) {
				url, ok := m["repoURL"].(string)
				if !ok {
					return
				}
				revision, _ := m["revision"].(string)
				if target, ok := m["targetRevision"].(string); ok {
					revision = target
				}
				if url == appTemplateRepo {
					assert.Regexp(t, `^\d+\.\d+\.\d+$`, revision, "env %q %s: app-template's version", env, name)
					return
				}
				homelab++
				assert.Equal(t, "{{ .metadata.annotations.repository }}", url, "env %q %s", env, name)
				assert.Equal(t, "{{ .metadata.annotations.revision }}", revision, "env %q %s", env, name)
			})
			assert.Positive(t, homelab, "env %q %s", env, name)
			if name == "root" {
				assert.Equal(t, "{{ .metadata.annotations.backupS3Url }}",
					dig(spec, "template", "spec", "sources", 0, "helm", "valuesObject", "global", "backupS3Url"), "env %q", env)
			}
			raw, err := json.Marshal(set)
			require.NoError(t, err)
			for _, literal := range []string{"github.com/duongle-steven/homelab", "seed-repository.invalid"} {
				assert.False(t, strings.Contains(string(raw), literal), "env %q %s names %s", env, name, literal)
			}
			// Review Focus 5: a template naming an annotation the Secret does not write fails here, not in the cluster
			for _, match := range known.FindAllStringSubmatch(string(raw), -1) {
				assert.Contains(t, annotations, match[1], "env %q %s reads an annotation cluster-secret.sh does not write", env, name)
			}
		}
	}
}

// The sandbox's optional apps come only through their own generators, each selecting the cluster Secret's label
func TestArgoCDOptionalApps(t *testing.T) {
	apps := asList(dig(renderAppSets(t, "dev")["apps"], "spec", "generators"))
	optional := map[string]bool{}
	for _, g := range apps {
		labels, _ := dig(g, "matrix", "generators", 0, "clusters", "selector", "matchLabels").(map[string]any)
		var files []string
		walk(dig(g, "matrix", "generators", 1), func(m map[string]any) {
			for _, f := range asList(m["files"]) {
				files = append(files, f.(map[string]any)["path"].(string))
			}
		})
		app := ""
		for key := range labels {
			if name, ok := strings.CutPrefix(key, "homelab/app-"); ok {
				app = name
			}
		}
		if app == "" {
			assert.NotContains(t, files, "apps/paperless.yaml")
			assert.NotContains(t, files, "apps/opencloud.yaml")
			continue
		}
		optional[app] = true
		assert.Equal(t, map[string]any{"homelab/bootstrap": "true", "homelab/app-" + app: "true"}, labels)
		assert.Equal(t, []string{"apps/" + app + ".yaml", "infra/sandbox/cluster/config.json"}, files)
	}
	assert.Equal(t, map[string]bool{"paperless": true, "opencloud": true}, optional)

	raw, err := json.Marshal(renderAppSets(t, ""))
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(raw), "homelab/app-"), "production has no optional apps")
}

// dig follows keys (string for a map, int for a list) under v; nil when one is missing
func dig(v any, keys ...any) any {
	for _, key := range keys {
		switch key := key.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[key]
		case int:
			list := asList(v)
			if key >= len(list) {
				return nil
			}
			v = list[key]
		}
	}
	return v
}

func asList(v any) []any {
	list, _ := v.([]any)
	return list
}

// The bootstrap renders before it touches the cluster Secret: a failed render leaves the Secret as it was, and a
// changed Secret waits only for the CRDs, not the render, before the ApplicationSets that match it
func TestArgoCDBootstrapRendersBeforeSecret(t *testing.T) {
	dir, env := fakeBin(t, map[string]string{"kubectl": `case "$*" in
  *"create namespace"*) printf 'apiVersion: v1\nkind: Namespace\nmetadata: {name: argocd}\n' ;;
  *wait*) ;;
  *apply*) yq -N '.kind + "/" + .metadata.name' >> "$FIXTURES/applied" ;;
  *) exit 1 ;;
esac`})
	root, err := filepath.Abs(repo.Root())
	require.NoError(t, err)
	cmd := exec.Command("sh", root+"/infra/_modules/argocd/bootstrap.sh")
	cmd.Env = append(env, "CHART="+root+"/system/argocd", "VALUES=values-missing.yaml", "ENVIRONMENT=dev",
		"REPOSITORY=https://github.com/example/homelab", "BRANCH=sandbox", "KUBECONFIG=/dev/null")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, string(out))
	applied, _ := os.ReadFile(filepath.Join(dir, "applied"))
	assert.NotContains(t, string(applied), "Secret/in-cluster", "the Secret changed although the render failed")
}
