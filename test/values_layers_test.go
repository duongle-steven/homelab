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
}
