package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Staging and the sandbox share values-local.yaml (the local CA, the tests' fixtures); only the sandbox loads
// values-dev.yaml's shrinks; production loads neither (docs/superpowers/specs/2026-11-01-staging-platform-design.md)
func TestValuesLayers(t *testing.T) {
	const bundle = `select(.kind == "Bundle" and .metadata.name == "homelab-dev-ca") | .metadata.name`
	assert.Equal(t, "homelab-dev-ca", renderYq(t, "system/cert-manager", "staging", bundle), "staging has the local CA")
	assert.Equal(t, "homelab-dev-ca", renderYq(t, "system/cert-manager", "dev", bundle), "the sandbox has the local CA")
	assert.Empty(t, renderYq(t, "system/cert-manager", "", bundle), "production has no local CA")

	const retention = `select(.kind == "Prometheus") | .spec.retention`
	assert.Equal(t, "10d", renderYq(t, "system/monitoring-system", "staging", retention), "staging keeps production's retention")
	assert.Equal(t, "1d", renderYq(t, "system/monitoring-system", "dev", retention), "the sandbox keeps one day")

	// Kyverno's and the Trivy Operator's load on the API server is limited on every environment but production: small
	// nodes lost their leases under it (the sandbox 2026-10-27, staging 2026-11-01)
	const scanInterval = `select(.kind == "Deployment" and .metadata.name == "kyverno-background-controller" or .kind == "Deployment" and .metadata.name == "kyverno-reports-controller") | .spec.template.spec.containers[0].args[] | select(test("backgroundScanInterval"))`
	for _, env := range []string{"staging", "dev"} {
		assert.Contains(t, renderYq(t, "system/kyverno", env, scanInterval), "6h", env)
		assert.Empty(t, renderYq(t, "system/kyverno", env, `select(.kind == "Deployment" and .metadata.name == "kyverno-cleanup-controller") | .metadata.name`), env)
		assert.Equal(t, "1", renderYq(t, "system/trivy-operator", env,
			`select(.kind == "ConfigMap") | .data.OPERATOR_CONCURRENT_SCAN_JOBS_LIMIT | select(. != null)`), env)
	}
}
