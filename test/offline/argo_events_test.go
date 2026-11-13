package offline

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Argo Events' controller (step 10f): no admission webhook (nothing in the API server's path depends on it), its pod
// confined like every workload, its CRDs installed with it
func TestArgoEventsRender(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		render := func(expr string) string { return renderVelero(t, "platform/argo-events", env, expr) }
		assert.Empty(t, render(`select(.kind == "ValidatingWebhookConfiguration") | .metadata.name`), env)
		assert.Equal(t, "RuntimeDefault", render(`select(.kind == "Deployment") | .spec.template.spec.securityContext.seccompProfile.type`), env)
		assert.Equal(t, "false", render(`select(.kind == "Deployment") | .spec.template.spec.containers[0].securityContext.allowPrivilegeEscalation`), env)
		assert.Contains(t, render(`select(.kind == "CustomResourceDefinition") | .metadata.name`), "eventbus.argoproj.io", env)
	}
}
