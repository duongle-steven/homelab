package offline

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// On a fresh install apps sync while ESO's webhook has no endpoint yet; with failurePolicy Fail the API server refused
// their SecretStore and ExternalSecrets, and Argo CD waited forever for workloads that needed those Secrets (zot, and
// step 10e's CI)
func TestExternalSecretsWebhookIgnoredWhileDown(t *testing.T) {
	cmd := exec.Command("sh", "-c", `scripts/render-chart platform/external-secrets | yq -N 'select(.kind == "ValidatingWebhookConfiguration") | .webhooks[] | .name + " " + .failurePolicy'`)
	cmd.Dir = repo.Root()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	require.NotEmpty(t, lines)
	for _, l := range lines {
		assert.True(t, strings.HasSuffix(l, " Ignore"), l)
	}
}
