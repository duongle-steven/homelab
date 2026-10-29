package test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderTrivyOperator renders system/trivy-operator for env ("" or dev) and runs a yq expression on it
func renderTrivyOperator(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/trivy-operator $1 | yq -N "$0"`, expr, env)
	cmd.Dir = ".."
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// docs/superpowers/specs/2026-10-29-supply-chain-design.md: only the scans nothing else covers, every finding reported
// (fixed or not), acceptances with a reason and an expiry, three alerts
func TestTrivyOperatorRender(t *testing.T) {
	for _, e := range []string{"", "dev"} {
		t.Run("env="+e, func(t *testing.T) {
			config := `select(.kind == "ConfigMap" and .metadata.name == "trivy-operator-config") | .data`
			for key, want := range map[string]string{
				"OPERATOR_VULNERABILITY_SCANNER_ENABLED": "true", "OPERATOR_EXPOSED_SECRET_SCANNER_ENABLED": "true",
				"OPERATOR_CONFIG_AUDIT_SCANNER_ENABLED": "false", "OPERATOR_RBAC_ASSESSMENT_SCANNER_ENABLED": "false",
				"OPERATOR_INFRA_ASSESSMENT_SCANNER_ENABLED": "false", "OPERATOR_CLUSTER_COMPLIANCE_ENABLED": "false",
				"OPERATOR_SBOM_GENERATION_ENABLED": "false", "OPERATOR_METRICS_VULN_ID_ENABLED": "true",
				"OPERATOR_BUILT_IN_TRIVY_SERVER": "true",
			} {
				assert.Equal(t, want, renderTrivyOperator(t, e, config+` | .["`+key+`"]`), key)
			}
			trivy := `select(.kind == "ConfigMap" and .metadata.name == "trivy-operator-trivy-config") | .data`
			assert.NotEqual(t, "true", renderTrivyOperator(t, e, trivy+` | .["trivy.ignoreUnfixed"]`), "unfixed findings are reported too")
			assert.Equal(t, "HIGH,CRITICAL", renderTrivyOperator(t, e, trivy+` | .["trivy.severity"]`))
			assert.Empty(t, renderTrivyOperator(t, e, `select(.kind == "PersistentVolumeClaim" or .kind == "StatefulSet") | .spec.volumeClaimTemplates[]?.metadata.name`))

			// Every acceptance (Trivy's .trivyignore.yaml, as 15a's): a statement and an expiry still to come
			assert.Equal(t, ".trivyignore.yaml", renderTrivyOperator(t, e, trivy+` | .["trivy.ignoreFileName"]`))
			ignore := trivy + ` | .["trivy.ignoreFile"] | from_yaml | .vulnerabilities // [] | .[]`
			for _, entry := range strings.Fields(renderTrivyOperator(t, e, ignore+` | .id + "|" + (.expired_at // "") + "|" + ((.statement // "") | length | tostring)`)) {
				parts := strings.Split(entry, "|")
				at, err := time.Parse("2006-01-02", parts[1])
				if assert.NoError(t, err, "%s has no expired_at", parts[0]) {
					assert.True(t, at.After(time.Now()), "%s expired", parts[0])
				}
				assert.NotEqual(t, "0", parts[2], "%s has no statement", parts[0])
			}

			rule := func(name, field string) string {
				return renderTrivyOperator(t, e, `select(.kind == "PrometheusRule") | .spec.groups[].rules[] | select(.alert == "`+name+`") | `+field)
			}
			for _, name := range []string{"ImageCriticalVulnerability", "ImageCriticalVulnerabilityUnfixed", "ImageExposedSecret"} {
				assert.Equal(t, "1h", rule(name, ".for"), name)
				assert.Equal(t, "warning", rule(name, ".labels.severity"), name)
				assert.Equal(t, "trivy", rule(name, ".labels.source"), name)
				assert.Contains(t, rule(name, ".expr"), "by (namespace, resource_kind, resource_name, image_repository)", name)
			}
			fixed, unfixed := rule("ImageCriticalVulnerability", ".expr"), rule("ImageCriticalVulnerabilityUnfixed", ".expr")
			assert.Contains(t, fixed, `trivy_vulnerability_id{severity="Critical", fixed_version!=""}`)
			assert.Contains(t, unfixed, `trivy_vulnerability_id{severity="Critical", fixed_version=""}`)
			assert.Equal(t, strings.Replace(fixed, `fixed_version!=""`, `fixed_version=""`, 1), unfixed, "the two differ only in the fix")
			assert.Contains(t, rule("ImageExposedSecret", ".expr"), "trivy_image_exposedsecrets")
		})
	}
	// The sandbox: one scan at a time, with limits
	assert.Equal(t, "1", renderTrivyOperator(t, "dev", `select(.kind == "ConfigMap" and .metadata.name == "trivy-operator-config") | .data["OPERATOR_CONCURRENT_SCAN_JOBS_LIMIT"]`))
	assert.NotEmpty(t, renderTrivyOperator(t, "dev", `select(.kind == "ConfigMap" and .metadata.name == "trivy-operator-trivy-config") | .data["trivy.resources.limits.memory"]`))
}
