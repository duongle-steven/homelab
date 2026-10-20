package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A generated secret is created once and never regenerated: its PushSecret writes OpenBao only if the key does not
// exist (IfNotExists), so a second value would stay in the source Secret while every consumer reads the first. A fresh
// sandbox once regenerated registry.ci after the push, and zot refused the ci account the tests read from that Secret.
func TestGlobalSecretsCreatedOnce(t *testing.T) {
	cmd := exec.Command("sh", "-c", `scripts/render-chart platform/global-secrets dev | yq -N 'select(.kind == "ExternalSecret") | .metadata.name + " " + (.spec.refreshPolicy // "unset")'`)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	require.NotEmpty(t, lines[0])
	for _, line := range lines {
		assert.True(t, strings.HasSuffix(line, " CreatedOnce"), "ExternalSecret %s", line)
	}
}
