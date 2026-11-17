package offline

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderSecretStore renders system/secret-store into namespace as Argo CD would on a cluster that does not have
// External Secrets' CRDs yet: no --api-versions
func renderSecretStore(t *testing.T, namespace string) string {
	t.Helper()
	cmd := exec.Command("helm", "template", "secret-store", "system/secret-store", "--namespace", namespace)
	cmd.Dir = repo.Root()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return string(out)
}

// A fresh install's Applications may render before External Secrets' CRDs exist. The SecretStore used to depend on
// them being there (.Capabilities), and on GitHub Actions' fresh sandboxes eight namespaces never got theirs (PR #98's
// first run, #104's): their Secrets never came and their pods waited. It is rendered whatever the cluster has, and Argo
// CD retries it until the CRD exists
func TestSecretStoreWithoutItsCRD(t *testing.T) {
	out := renderSecretStore(t, "grafana")
	assert.Contains(t, out, "kind: SecretStore")
	assert.Contains(t, out, "argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true")
	// Still none where the mesh and egress are not managed either
	assert.NotContains(t, renderSecretStore(t, "istio-system"), "kind: SecretStore")
}
