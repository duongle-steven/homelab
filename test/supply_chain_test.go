package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	labImage         = "ghcr.io/duongle-steven/homelab-lab*"
	labImageIssuer   = "https://token.actions.githubusercontent.com"
	labImageWorkflow = "https://github.com/duongle-steven/homelab/.github/workflows/lab-image.yaml@refs/heads/master"
)

// renderKyverno renders system/kyverno for env ("" or dev) and runs a yq expression on it
func renderKyverno(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/kyverno $1 | yq -N "$0"`, expr, env)
	cmd.Dir = ".."
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// lab's image is signed by lab-image.yaml on master, keyless, with its SBOM attested; production's Kyverno checks
// both (Audit until 15b-2); the sandbox runs lab's image built from the checkout, unsigned, so it has no such policy
func TestLabImagePolicy(t *testing.T) {
	policy := `select(.kind == "ImageValidatingPolicy" and .metadata.name == "verify-lab-image")`
	assert.Equal(t, "verify-lab-image", renderKyverno(t, "", `[select(.kind == "ImageValidatingPolicy")] | map(.metadata.name) | join(",")`))
	assert.Equal(t, labImage, renderKyverno(t, "", policy+` | .spec.matchImageReferences[].glob`))
	assert.Equal(t, "Audit", renderKyverno(t, "", policy+` | .spec.validationActions | join(",")`))
	identity := policy + ` | .spec.attestors[] | select(.name == "github") | .cosign.keyless.identities[0]`
	// Exactly master's run of the workflow: a regex would let a branch's run pass
	assert.Equal(t, labImageIssuer, renderKyverno(t, "", identity+` | .issuer`))
	assert.Equal(t, labImageWorkflow, renderKyverno(t, "", identity+` | .subject`))
	assert.Equal(t, "null", renderKyverno(t, "", identity+` | .subjectRegExp`))
	assert.Equal(t, "https://spdx.dev/Document", renderKyverno(t, "", policy+` | .spec.attestations[] | select(.name == "sbom") | .intoto.type`))
	validations := renderKyverno(t, "", policy+` | [.spec.validations[].expression] | join(" ")`)
	assert.Contains(t, validations, "verifyImageSignatures")
	assert.Contains(t, validations, "verifyAttestationSignatures")

	assert.Empty(t, renderKyverno(t, "dev", `select(.kind == "ImageValidatingPolicy") | .metadata.name`))
}
