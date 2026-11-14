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

// The agent (a DaemonSet) reads every pod's log and the API server's audit log on its node, resumes from its positions
// after a restart or a rotation, tells OpenBao's audit lines from its other output, and sends to Loki itself through a
// queue kept on the node, so lines read while Loki is down survive its restart (Alloy's write-ahead log did)
func TestCollectorAgentRender(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		agent := func(expr string) string {
			return renderVelero(t, "system/opentelemetry", env,
				`select(.kind == "OpenTelemetryCollector" and .metadata.name == "agent") | `+expr)
		}
		assert.Equal(t, "daemonset", agent(`.spec.mode`), env)
		config := `.spec.config`
		assert.Equal(t, "/var/log/pods/*/*/*.log", agent(config+`.receivers["filelog/pods"].include[]`), env)
		assert.Equal(t, "container", agent(config+`.receivers["filelog/pods"].operators[0].type`), env)
		assert.Equal(t, "/var/log/kubernetes/audit/audit.log", agent(config+`.receivers["filelog/audit"].include[]`), env)
		for _, receiver := range []string{"filelog/pods", "filelog/audit"} {
			// Positions persist: a restart or the API server's rotation neither rereads nor skips a line
			assert.Equal(t, "beginning file_storage", agent(config+`.receivers["`+receiver+`"] | .start_at + " " + .storage`), env, receiver)
		}
		assert.Equal(t, "/var/lib/otelcol", agent(config+`.extensions.file_storage.directory`), env)
		processors := agent(config + `.service.pipelines | to_entries | .[].value.processors[]`)
		assert.Contains(t, processors, "k8sattributes", env)
		transform := agent(config + `.processors | to_entries | map(select(.key | test("^transform"))) | .[].value | to_json(0)`)
		assert.Contains(t, transform, `\"k3s-audit\"`, env)
		assert.Contains(t, transform, `\"openbao-audit\"`, env)
		// Only OpenBao's audit JSON is openbao-audit: a routing connector moves those records alone into their own
		// pipeline (a resource attribute set on a mixed batch would name OpenBao's other output audit too)
		route := agent(config + `.connectors["routing/openbao"] | to_json(0)`)
		assert.Contains(t, route, `k8s.namespace.name\"] == \"openbao\"`, env)
		assert.Contains(t, route, `\"type\\\":\\\"(request|response)\\\"`, env)
		exporter := config + `.exporters["otlphttp/loki"]`
		assert.Equal(t, "http://loki.loki:3100/otlp", agent(exporter+`.endpoint`), env)
		assert.Equal(t, "file_storage", agent(exporter+`.sending_queue.storage`), env)
		mounts := agent(`.spec.volumeMounts[] | .mountPath + " " + ((.readOnly // false) | tostring)`)
		assert.Contains(t, mounts, "/var/log/pods true", env)
		assert.Contains(t, mounts, "/var/log/kubernetes/audit true", env)
		assert.Contains(t, mounts, "/var/lib/otelcol false", env)
	}
}

// Alloy is gone; Loki takes OTLP
func TestLokiWithoutAlloy(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		names := renderVelero(t, "system/loki", env, `.metadata.name`)
		assert.NotContains(t, strings.ToLower(names), "alloy", env)
		assert.NotEqual(t, "null", renderVelero(t, "system/loki", env,
			`select(.kind == "ConfigMap" and .metadata.name == "loki") | .data["config.yaml"] | from_yaml | .limits_config.otlp_config`), env)
	}
}

