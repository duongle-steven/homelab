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
		// The image runs as root unless told otherwise; runAsNonRoot alone refuses to start it (the sandbox, 2026-11-13)
		assert.Equal(t, "65532", render(`select(.kind == "Deployment") | .spec.template.spec.securityContext.runAsUser`), env)
		assert.Equal(t, "false", render(`select(.kind == "Deployment") | .spec.template.spec.containers[0].securityContext.allowPrivilegeEscalation`), env)
		assert.Contains(t, render(`select(.kind == "CustomResourceDefinition") | .metadata.name`), "eventbus.argoproj.io", env)
		// The JetStream version platform/ci's EventBus names, on NATS's latest images: the chart's own list (nats 2.10.29,
		// the exporter and reloader 0.14.0) carries CRITICAL CVEs with fixes (the sandbox's TrivyOperator spec)
		jetstream := `select(.kind == "ConfigMap" and .metadata.name == "argo-events-controller-manager") | .data["controller-config.yaml"] | from_yaml | .eventBus.jetstream.versions[]`
		assert.Equal(t, "2.15.3 nats:2.15.3 natsio/prometheus-nats-exporter:0.20.3 natsio/nats-server-config-reloader:0.25.0 /nats-server",
			render(jetstream+` | select(.version == "2.15.3") | .version + " " + .natsImage + " " + .metricsExporterImage + " " + .configReloaderImage + " " + .startCommand`), env)
	}
}
