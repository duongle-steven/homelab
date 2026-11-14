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
		telemetry := `select(.kind == "Telemetry" and .metadata.name == "gateways") | .spec`
		assert.Equal(t, "gateway,public", render(telemetry+` | [.targetRefs[] | select(.kind == "Gateway") | .name] | join(",")`), env)
		assert.Equal(t, "otel-tracing "+sampling, render(telemetry+` | .tracing[] | .providers[0].name + " " + (.randomSamplingPercentage | tostring)`), env)
		assert.Equal(t, "otel-json", render(telemetry+` | .accessLogging[].providers[0].name`), env)
	}
}
