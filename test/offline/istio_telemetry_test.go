package offline

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The Istio Gateways send their spans to the gateway Collector and write JSON access logs carrying the trace id, the
// lines Grafana opens a trace from (step OpenTelemetry); every request is sampled in the sandbox, a tenth in production
func TestIstioTracingRender(t *testing.T) {
	for env, sampling := range map[string]string{"": "10", "dev": "100"} {
		render := func(expr string) string { return renderVelero(t, "system/istio-system", env, expr) }
		mesh := `select(.kind == "ConfigMap" and .metadata.name == "istio") | .data.mesh | from_yaml | .extensionProviders[]`
		assert.Equal(t, "gateway-collector.opentelemetry.svc.cluster.local 4317",
			render(mesh+` | select(.name == "otel-tracing") | .opentelemetry.service + " " + (.opentelemetry.port | tostring)`), env)
		assert.Equal(t, "/dev/stdout %TRACE_ID%",
			render(mesh+` | select(.name == "otel-json") | .envoyFileAccessLog.path + " " + .envoyFileAccessLog.logFormat.labels.trace_id`), env)
		// The path without its query string: a query can carry credentials (OAuth codes, ntfy's ?auth=), and access
		// logs are kept 30 days
		assert.Equal(t, "%REQ_WITHOUT_QUERY(X-ENVOY-ORIGINAL-PATH?:PATH)%",
			render(mesh+` | select(.name == "otel-json") | .envoyFileAccessLog.logFormat.labels.path`), env)
		telemetry := `select(.kind == "Telemetry" and .metadata.name == "gateways") | .spec`
		assert.Equal(t, "gateway,public", render(telemetry+` | [.targetRefs[] | select(.kind == "Gateway") | .name] | join(",")`), env)
		assert.Equal(t, "otel-tracing "+sampling, render(telemetry+` | .tracing[] | .providers[0].name + " " + (.randomSamplingPercentage | tostring)`), env)
		assert.Equal(t, "otel-json", render(telemetry+` | .accessLogging[].providers[0].name`), env)
	}
}

// The testbed caps istiod at its two replicas (docs/installation/testbed.md, 2026-11-26): under the suite's load its HPA
// went to five on three 4-vCPU VMs, taking CPU from etcd and Ceph; production keeps room to scale
func TestIstiodReplicasPerEnvironment(t *testing.T) {
	for env, max := range map[string]string{"": "5", "testbed": "2"} {
		hpa := renderVelero(t, "system/istio-system", env, `select(.kind == "HorizontalPodAutoscaler" and .metadata.name == "istiod") | (.spec.minReplicas | tostring) + "/" + (.spec.maxReplicas | tostring)`)
		assert.Equal(t, "2/"+max, hpa, "env %q", env)
	}
}
