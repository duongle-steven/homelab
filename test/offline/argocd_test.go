package offline

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing a directory from root must never delete its resources: when Argo CD stopped managing itself, root dropped
// Application argocd, and its resources finalizer deleted part of Argo CD before the bootstrap could release it
func TestArgoCDRootPreservesResources(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		cmd := exec.Command("sh", "-c", `scripts/render-chart system/argocd "$0" | yq 'select(.kind == "ApplicationSet" and .metadata.name == "root") | .spec.syncPolicy.preserveResourcesOnDeletion'`, env)
		cmd.Dir = repo.Root()
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
		cmd.Dir = repo.Root()
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
