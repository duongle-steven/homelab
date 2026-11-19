package offline

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderArgoRollouts renders system/argo-rollouts for env ("" or dev) and runs a yq expression on it
func renderArgoRollouts(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/argo-rollouts $1 | yq -N "$0"`, expr, env)
	cmd.Dir = repo.Root()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// docs/superpowers/specs/2026-11-18-app-delivery-design.md: the controller with the Gateway API traffic-router plugin,
// pinned by its checksum, one shared analysis on the Gateways' metrics, and an alert when a rollout aborts
func TestArgoRolloutsRender(t *testing.T) {
	for _, e := range []string{"", "dev"} {
		t.Run("env="+e, func(t *testing.T) {
			plugins := `select(.kind == "ConfigMap" and .metadata.name == "argo-rollouts-config") | .data.trafficRouterPlugins | from_yaml | .[]`
			assert.Equal(t, "argoproj-labs/gatewayAPI", renderArgoRollouts(t, e, plugins+` | .name`))
			assert.True(t, strings.HasSuffix(renderArgoRollouts(t, e, plugins+` | .location`), "/v0.18.0/gatewayapi-plugin-linux-amd64"))
			assert.Regexp(t, `^[0-9a-f]{64}$`, renderArgoRollouts(t, e, plugins+` | .sha256`))

			analysis := `select(.kind == "ClusterAnalysisTemplate" and .metadata.name == "gateway-success-rate")`
			assert.Equal(t, "max-error-ratio,max-p95-ms,namespace,service",
				renderArgoRollouts(t, e, analysis+` | [.spec.args[].name] | sort | join(",")`))
			queries := renderArgoRollouts(t, e, analysis+` | [.spec.metrics[].provider.prometheus.query] | join("\n")`)
			assert.Contains(t, queries, "istio_requests_total")
			assert.Contains(t, queries, "istio_request_duration_milliseconds_bucket")
			assert.Contains(t, queries, `destination_service_name="{{args.service}}"`)

			assert.Equal(t, "RolloutAborted", renderArgoRollouts(t, e,
				`select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert == "RolloutAborted") | .alert`))
			// The chart's own Gateway API rules: the plugin moves weights on the app's HTTPRoute
			assert.Contains(t, renderArgoRollouts(t, e,
				`select(.kind == "ClusterRole" and .metadata.name == "argo-rollouts") | .rules[] | select(.apiGroups[] == "gateway.networking.k8s.io") | .verbs | join(",")`),
				"update")
			// Only the Gateway API among the providers
			assert.Empty(t, renderArgoRollouts(t, e,
				`select(.kind == "ClusterRole" and .metadata.name == "argo-rollouts") | .rules[] | select(.apiGroups[] == "networking.istio.io") | .verbs | join(",")`))
			assert.Empty(t, renderArgoRollouts(t, e, `select(.kind == "Deployment" and .metadata.name == "argo-rollouts-dashboard") | .metadata.name`))
			assert.Equal(t, "RuntimeDefault", renderArgoRollouts(t, e,
				`select(.kind == "Deployment") | .spec.template.spec.containers[0].securityContext.seccompProfile.type`))
			assert.Equal(t, "argo-rollouts", renderArgoRollouts(t, e,
				`select(.kind == "AuthorizationPolicy" and .metadata.name == "allow") | .metadata.namespace`))
		})
	}
}
