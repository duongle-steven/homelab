package offline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Tempo stores the traces (step OpenTelemetry): one binary on a PVC that is never backed up (telemetry, like Loki), a
// week in production and a day in the sandbox, OTLP from the gateway Collector only, its API for Grafana only
func TestTempoRender(t *testing.T) {
	for env, retention := range map[string]string{"": "168h", "dev": "24h"} {
		render := func(expr string) string { return renderVelero(t, "system/tempo", env, expr) }
		assert.Equal(t, "1", render(`select(.kind == "StatefulSet") | .spec.replicas | tostring`), env)
		// The chart cannot label its claim: scripts/check-backup-groups lists it as unlabelled (none)
		assert.Equal(t, "storage", render(`select(.kind == "StatefulSet") | .spec.volumeClaimTemplates[].metadata.name`), env)
		config := `select(.kind == "ConfigMap" and .metadata.name == "tempo") | .data["tempo.yaml"] | from_yaml`
		assert.Equal(t, retention, render(config+` | .backend_worker.compaction.block_retention`), env)
		assert.Contains(t, render(config+` | .distributor.receivers | keys | join(",")`), "otlp", env)
		assert.Equal(t, map[string]string{"": "1Gi 800MiB", "dev": "256Mi 200MiB"}[env], render(`select(.kind == "StatefulSet") | .spec.template.spec.containers[] | select(.name == "tempo") | .resources.limits.memory + " " + (.env[] | select(.name == "GOMEMLIMIT") | .value)`), env)
		policy := `select(.kind == "AuthorizationPolicy" and .metadata.name == "allow") | .spec.rules[] | ((.from[].source.principals // []) + (.from[].source.namespaces // []) | join(",")) + " " + ((.to // [] | .[].operation.ports // []) | join(","))`
		rules := render(policy)
		assert.Contains(t, rules, "cluster.local/ns/opentelemetry/sa/gateway-collector 4317", env)
		assert.Contains(t, rules, "cluster.local/ns/grafana/sa/grafana 3200", env)
		// The chart's Jaeger receivers stay (its templates require them), but the mesh admits nothing to their ports
		ports := render(`select(.kind == "AuthorizationPolicy" and .metadata.name == "allow") | .spec.rules[].to[].operation.ports[]`)
		assert.ElementsMatch(t, []string{"4317", "3200", "3200"}, strings.Fields(ports), env)
	}
}

// Grafana reads traces from Tempo and links them with Loki's logs both ways by trace id
func TestGrafanaTraceDataSources(t *testing.T) {
	sources := `select(.kind == "ConfigMap" and (.metadata.name | test("datasource"))) | .data[] | from_yaml | .datasources[]`
	for _, env := range []string{"", "dev"} {
		render := func(expr string) string { return renderVelero(t, "system/monitoring-system", env, expr) }
		assert.Equal(t, "tempo http://tempo.tempo:3200", render(sources+` | select(.uid == "tempo") | .type + " " + .url`), env)
		assert.Equal(t, "loki", render(sources+` | select(.uid == "tempo") | .jsonData.tracesToLogsV2.datasourceUid`), env)
		assert.Equal(t, "trace_id tempo", render(sources+` | select(.uid == "loki") | .jsonData.derivedFields[] | .name + " " + .datasourceUid`), env)
	}
}
