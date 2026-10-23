package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// render renders system/argocd for the sandbox the way the bootstrap does, through set-revision.sh
func renderArgoCD(t *testing.T, revision string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/argocd dev | sh infra/_modules/argocd/set-revision.sh "$0"`, revision)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

// The bootstrap points every source and generator of the homelab repository at the chosen commit, and nothing else
func TestArgoCDRevision(t *testing.T) {
	plain := renderArgoCD(t, "")
	require.Contains(t, plain, "revision: sandbox")
	pinned := renderArgoCD(t, "abc123")
	assert.NotContains(t, pinned, "revision: sandbox")
	assert.NotContains(t, pinned, "targetRevision: sandbox")
	assert.Contains(t, pinned, "revision: abc123")
	assert.Contains(t, pinned, "targetRevision: abc123")
	// app-template's chart version is another repository's revision
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "targetRevision:") && !strings.Contains(line, "sandbox") {
			assert.Contains(t, pinned, line, "a revision of another repository changed")
		}
	}
	assert.Equal(t, strings.Count(plain, "sandbox"), strings.Count(pinned, "abc123")+strings.Count(pinned, "sandbox"),
		"every homelab revision was rewritten, and only those")
}

// Removing a directory from root must never delete its resources: when Argo CD stopped managing itself, root dropped
// Application argocd, and its resources finalizer deleted part of Argo CD before the bootstrap could release it
func TestArgoCDRootPreservesResources(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		cmd := exec.Command("sh", "-c", `scripts/render-chart system/argocd "$0" | yq 'select(.kind == "ApplicationSet" and .metadata.name == "root") | .spec.syncPolicy.preserveResourcesOnDeletion'`, env)
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		assert.Equal(t, "true", strings.TrimSpace(string(out)), "env %q", env)
	}
}

// Argo CD's ServiceMonitors come from system/argocd-config, which Argo CD syncs once monitoring's CRDs exist: the
// bootstrap renders system/argocd without them, so the chart's own ServiceMonitors would never be applied
func TestArgoCDServiceMonitors(t *testing.T) {
	render := func(chart string) string {
		cmd := exec.Command("sh", "-c", `scripts/render-chart "$0" dev`, chart)
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	yq := func(manifests, expr string) string {
		cmd := exec.Command("yq", "-N", expr)
		cmd.Stdin = strings.NewReader(manifests)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	core := render("system/argocd")
	assert.Empty(t, yq(core, `select(.kind == "ServiceMonitor") | .metadata.name`), "system/argocd renders ServiceMonitors")
	config := render("system/argocd-config")
	monitors := yq(config, `select(.kind == "ServiceMonitor") | .metadata.name`)
	assert.ElementsMatch(t, []string{"argocd-application-controller", "argocd-repo-server", "argocd-server", "argocd-redis"},
		strings.Fields(monitors))
	// Each selector matches a metrics Service of the chart, on a port that Service has
	for _, name := range strings.Fields(monitors) {
		component := yq(config, `select(.kind == "ServiceMonitor" and .metadata.name == "`+name+`") | .spec.selector.matchLabels."app.kubernetes.io/component"`)
		port := yq(config, `select(.kind == "ServiceMonitor" and .metadata.name == "`+name+`") | .spec.endpoints[0].port`)
		service := yq(core, `select(.kind == "Service" and .metadata.labels."app.kubernetes.io/component" == "`+component+`" and (.spec.ports[].name == "`+port+`")) | .metadata.name`)
		assert.NotEmpty(t, service, "%s selects no metrics Service with port %s", name, port)
	}
}

// Production's bootstrap passes R2's endpoint (never in git) to the root ApplicationSet's components as
// global.backupS3Url, which system/velero's location reads; without one the manifests are unchanged
func TestArgoCDBackupEndpoint(t *testing.T) {
	render := func(endpoint string) string {
		cmd := exec.Command("sh", "-c", `scripts/render-chart system/argocd | sh infra/_modules/argocd/set-backup-endpoint.sh "$0"`, endpoint)
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	get := func(manifests, expr string) string {
		cmd := exec.Command("yq", expr)
		cmd.Stdin = strings.NewReader(manifests)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	plain := render("")
	set := render("https://account.r2.cloudflarestorage.com")
	root := `select(.kind == "ApplicationSet" and .metadata.name == "root") | .spec.template.spec.sources[0].helm.valuesObject.global.backupS3Url`
	assert.Equal(t, "null", get(plain, root))
	assert.Equal(t, "https://account.r2.cloudflarestorage.com", get(set, root))
	// Only that value changes
	assert.Equal(t, get(plain, `del(select(.kind == "ApplicationSet" and .metadata.name == "root"))`),
		get(set, `del(select(.kind == "ApplicationSet" and .metadata.name == "root"))`))
}
