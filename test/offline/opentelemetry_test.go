package offline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The OpenTelemetry Operator runs the Collectors (step OpenTelemetry): its own image, the Collector distribution it
// defaults to, and a pod webhook that never blocks a pod while the Operator is down; the API server calls its webhooks,
// so its pods are outside the mesh
func TestOpenTelemetryOperatorRender(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		render := func(expr string) string { return renderVelero(t, "system/opentelemetry", env, expr) }
		manager := `select(.kind == "Deployment") | .spec.template`
		assert.Contains(t, render(manager+` | .spec.containers[] | select(.name == "manager") | .image`), ":0.162.0", env)
		assert.Contains(t, render(manager+` | .spec.containers[] | select(.name == "manager") | .args[]`),
			"--collector-image=ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-k8s:0.165.0", env)
		assert.Equal(t, "none", render(manager+` | .metadata.labels["istio.io/dataplane-mode"]`), env)
		assert.Equal(t, "RuntimeDefault", render(manager+` | .spec.securityContext.seccompProfile.type`), env)
		policies := render(`select(.kind == "MutatingWebhookConfiguration") | .webhooks[] | .name + " " + .failurePolicy`)
		if assert.NotEmpty(t, policies, env) {
			for _, line := range strings.Split(policies, "\n") {
				assert.True(t, strings.HasSuffix(line, " Ignore"), "%s: %s", env, line)
			}
		}
		crds := render(`select(.kind == "CustomResourceDefinition") | .metadata.name`)
		assert.Contains(t, crds, "opentelemetrycollectors.opentelemetry.io", env)
		assert.Contains(t, crds, "instrumentations.opentelemetry.io", env)
	}
}
