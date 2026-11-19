package offline

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A fresh install's Argo CD renders system/argocd-config before kaniop's, External Secrets' and monitoring's CRDs exist.
// Its templates used to depend on them (.Capabilities): the render came out empty, Argo CD cached it as Synced, and on
// GitHub Actions' fresh sandboxes Argo CD had no Kanidm client and no Forgejo repository for an hour or more (#104).
// Rendered with no --api-versions, every resource is there, each retried by Argo CD until its CRD exists
func TestArgoCDConfigWithoutItsCRDs(t *testing.T) {
	cmd := exec.Command("helm", "template", "argocd-config", "system/argocd-config", "--namespace", "argocd",
		"--set", "global.baseDomain=example.com")
	cmd.Dir = repo.Root()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	for _, kind := range []string{"kind: KanidmOAuth2Client", "kind: ExternalSecret", "kind: ServiceMonitor"} {
		assert.Contains(t, string(out), kind)
	}
	resources := strings.Count(string(out), "\nkind: ")
	assert.Equal(t, resources, strings.Count(string(out), "argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true"),
		"every resource waits for its CRD")
}

// Argo CD reads our apps' versions from ops/releases with argocd's read-only token (docs/how-to-guides/app-delivery.md)
func TestArgoCDReadsReleases(t *testing.T) {
	cmd := exec.Command("sh", "-c", `helm template argocd-config system/argocd-config --namespace argocd --set global.baseDomain=example.com | yq 'select(.kind == "ExternalSecret" and .metadata.name == "argocd-repo-releases")'`)
	cmd.Dir = repo.Root()
	out, err := cmd.Output()
	require.NoError(t, err)
	s := string(out)
	assert.Contains(t, s, "url: http://forgejo-http.forgejo:3000/ops/releases")
	assert.Contains(t, s, "argocd.argoproj.io/secret-type: repository")
	assert.Contains(t, s, "key: argocd/forgejo")
	assert.Contains(t, s, "SkipDryRunOnMissingResource=true")
}
