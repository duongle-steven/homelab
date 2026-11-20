package offline

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderArgoRollouts renders system/argo-rollouts for env ("" or dev) and runs a yq expression on it
func renderArgoRollouts(t *testing.T, env, expr string) string {
	t.Helper()
	return renderVelero(t, "system/argo-rollouts", env, expr)
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
			assert.Equal(t, "max-error-ratio,max-p95-ms,namespace,port,service",
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

// gateway-success-rate judges a canary on data: its first measurement waits for a minute's rate to have two scrapes,
// and its last falls inside the test app's canary pauses (measured at once it found nothing and passed a canary
// serving only errors, AppDelivery 2026-11-20)
func TestGatewaySuccessRateMeasuresTheCanary(t *testing.T) {
	seconds := func(d string) float64 {
		v, err := time.ParseDuration(d)
		require.NoError(t, err, d)
		return v.Seconds()
	}
	metrics := strings.Split(renderVelero(t, "system/argo-rollouts", "", `select(.kind == "ClusterAnalysisTemplate" and .metadata.name == "gateway-success-rate") | .spec.metrics[] | select(.provider.prometheus) | .name + " " + (.initialDelay // "0s") + " " + .interval + " " + (.count | tostring)`), "\n")
	pauses := 0.0
	for _, p := range strings.Fields(renderVelero(t, "test/fixtures/apps/delivery-test.yaml", "dev", `select(.kind == "Rollout") | .spec.strategy.canary.steps[] | select(has("pause")) | .pause.duration`)) {
		pauses += seconds(p)
	}
	require.NotZero(t, pauses)
	for _, m := range metrics {
		f := strings.Fields(m)
		require.Len(t, f, 4, m)
		count, err := strconv.Atoi(f[3])
		require.NoError(t, err)
		assert.GreaterOrEqual(t, seconds(f[1]), 30.0, "%s measures before a minute's rate has two scrapes", f[0])
		assert.GreaterOrEqual(t, count, 3, "%s: too few measurements to judge", f[0])
		assert.LessOrEqual(t, seconds(f[1])+float64(count-1)*seconds(f[2]), pauses, "%s measures after the canary ended", f[0])
	}
}

// RolloutAborted reads the phase the controller reports for an aborted rollout (rollout_info{phase="Abort"}; never
// "Degraded") and names the rollout's namespace, which Prometheus keeps as exported_namespace beside the controller's
// own (AppDelivery 2026-11-20: the alert never fired)
func TestRolloutAbortedReadsTheController(t *testing.T) {
	expr := renderArgoRollouts(t, "", `select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert == "RolloutAborted") | .expr`)
	// A canary that never becomes healthy ends Timeout (progressDeadlineAbort), a broken spec Error or InvalidSpec
	assert.Contains(t, expr, `phase=~"Abort|Timeout|Error|InvalidSpec"`)
	assert.Equal(t, "true", renderVelero(t, "test/fixtures/apps/delivery-test.yaml", "dev", `select(.kind == "Rollout") | .spec.progressDeadlineAbort`))
	assert.Contains(t, expr, `"namespace", "$1", "exported_namespace"`)
}

// KargoPromotionFailed reads the Promotions' phase, which kube-state-metrics exports from promotions.kargo.akuity.io
// (Kargo itself was never scraped, and a failing step is no reconcile error: the alert could never fire, the app
// delivery review)
func TestKargoPromotionFailedReadsThePhase(t *testing.T) {
	ksm := `select(.kind == "ConfigMap" and (.data["config.yaml"] // "" | test("kargo.akuity.io"))) | .data["config.yaml"]`
	config := renderVelero(t, "system/monitoring-system", "", ksm)
	assert.Contains(t, config, "kind: Promotion")
	assert.Contains(t, config, "metricNamePrefix: kargo_promotion")
	rules := renderVelero(t, "system/monitoring-system", "", `select(.kind == "ClusterRole" and (.metadata.name | test("kube-state-metrics"))) | .rules[] | select(.apiGroups[0] == "kargo.akuity.io") | .resources[]`)
	assert.Equal(t, "promotions", rules)
	expr := renderVelero(t, "platform/kargo", "", `select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert == "KargoPromotionFailed") | .expr`)
	// kube-state-metrics names the family after metricNamePrefix alone (no kube_customresource_ before it)
	assert.Contains(t, expr, "(kargo_promotion_phase{")
	assert.NotContains(t, expr, "kube_customresource_")
	assert.Contains(t, expr, `phase=~"Failed|Errored"`)
}

// A canary of a quiet app gets no visitors, and gateway-success-rate passes on no data: canary-responds requests the
// canary Service itself from a Job in the app's namespace and fails on a 5xx or no answer (the app delivery review: a
// broken blog would go out at night)
func TestGatewaySuccessRateRequestsTheCanary(t *testing.T) {
	metric := `select(.kind == "ClusterAnalysisTemplate" and .metadata.name == "gateway-success-rate") | .spec.metrics[] | select(.name == "canary-responds")`
	args := renderArgoRollouts(t, "", metric+` | .provider.job.spec.template.spec.containers[0].args[0]`)
	assert.Contains(t, args, "http://{{args.service}}.{{args.namespace}}:{{args.port}}")
	assert.Contains(t, args, "5*")
	assert.Equal(t, "0", renderArgoRollouts(t, "", metric+` | .failureLimit`))
	assert.Equal(t, "true", renderArgoRollouts(t, "", metric+` | .provider.job.spec.template.spec.securityContext.runAsNonRoot`))
	assert.Equal(t, "8080", renderVelero(t, "test/fixtures/apps/delivery-test.yaml", "dev",
		`select(.kind == "Rollout") | .spec.strategy.canary.analysis.args[] | select(.name == "port") | .value`))
}

// Kargo's webhook validates our Projects: they sync a wave after its webhook server (which needs its certificate),
// or a fresh cluster's first sync fails them and every retry's dry-run fails against a webhook with no endpoint, so the
// certificate is never applied (the sandbox, 2026-11-20)
func TestKargoProjectsAfterTheWebhook(t *testing.T) {
	// Production has no Project until the blog joins the road
	assert.Equal(t, "1", renderVelero(t, "platform/kargo", "dev", `select(.kind == "Project") | .metadata.annotations["argocd.argoproj.io/sync-wave"]`))
	for _, env := range []string{"", "dev"} {
		assert.Equal(t, "", renderVelero(t, "platform/kargo", env, `select(.kind == "Deployment" and .metadata.name == "kargo-webhooks-server") | .metadata.annotations["argocd.argoproj.io/sync-wave"] // ""`), env)
	}
}
