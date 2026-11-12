package offline

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderTrivyOperator renders system/trivy-operator for env ("" or dev) and runs a yq expression on it
func renderTrivyOperator(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/trivy-operator $1 | yq -N "$0"`, expr, env)
	cmd.Dir = repo.Root()
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

			// The operator reads Trivy's settings (the ignore file among them) only at start, and the chart's checksum
			// covers only its own ConfigMap: the pod carries this one's hash, so a change restarts it
			data := renderTrivyOperator(t, e, trivy+` | sort_keys(.) | to_json(0)`)
			want := fmt.Sprintf("%x", sha256.Sum256([]byte(data)))[:16]
			assert.Equal(t, want, renderTrivyOperator(t, e, `select(.kind == "Deployment") | .spec.template.metadata.annotations["homelab/trivy-config"]`),
				"podAnnotations homelab/trivy-config in values%s.yaml", map[string]string{"": "", "dev": "-dev"}[e])

			// Both databases from ghcr.io, like Trivy's image (the chart's Java DB default is mirror.gcr.io)
			assert.Equal(t, "ghcr.io/aquasecurity/trivy-db", renderTrivyOperator(t, e, trivy+` | .["trivy.dbRepository"]`))
			assert.Equal(t, "ghcr.io/aquasecurity/trivy-java-db", renderTrivyOperator(t, e, trivy+` | .["trivy.javaDbRepository"]`))

			// Every acceptance (Trivy's .trivyignore.yaml, as 15a's): a statement and an expiry still to come
			assert.Equal(t, ".trivyignore.yaml", renderTrivyOperator(t, e, trivy+` | .["trivy.ignoreFileName"]`))
			ignore := trivy + ` | .["trivy.ignoreFile"] | from_yaml | .vulnerabilities // [] | .[]`
			for _, entry := range strings.Fields(renderTrivyOperator(t, e, ignore+` | .id + "|" + (.expired_at // "") + "|" + ((.statement // "") | length | tostring)`)) {
				parts := strings.Split(entry, "|")
				// Helm renders the file with toYaml, so the date is a string, which Trivy reads only as RFC 3339 (a bare date
				// stops every scan Job)
				at, err := time.Parse(time.RFC3339, parts[1])
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
				// A bump leaves the old ReplicaSet at 0 replicas with its report: it must not keep the alert firing
				assert.Contains(t, rule(name, ".expr"), idleReplicaSets, name)
			}
			fixed, unfixed := rule("ImageCriticalVulnerability", ".expr"), rule("ImageCriticalVulnerabilityUnfixed", ".expr")
			assert.Contains(t, fixed, `trivy_vulnerability_id{severity="Critical", fixed_version!=""}`)
			assert.Contains(t, unfixed, `trivy_vulnerability_id{severity="Critical", fixed_version=""}`)
			assert.Equal(t, strings.Replace(fixed, `fixed_version!=""`, `fixed_version=""`, 1), unfixed, "the two differ only in the fix")
			assert.Contains(t, rule("ImageExposedSecret", ".expr"), "trivy_image_exposedsecrets")
			// A scan that fails (OOM, timeout, an image it cannot pull) leaves no report, so no finding: that alerts too
			missing := rule("ImageScanMissing", ".expr")
			assert.Equal(t, "6h", rule("ImageScanMissing", ".for"))
			assert.Equal(t, "warning", rule("ImageScanMissing", ".labels.severity"))
			for _, kind := range []string{"kube_replicaset_spec_replicas", "kube_statefulset_replicas", "kube_daemonset_status_desired_number_scheduled"} {
				assert.Contains(t, missing, kind+" > 0")
			}
			assert.Contains(t, missing, "unless on (namespace, resource_name) trivy_image_vulnerabilities")
		})
	}
	// The sandbox: two scans at a time (one queued about 90 minutes of scans behind the suite's 45-minute wait, 12b-2),
	// with limits; the testbed keeps one (values-local.yaml)
	assert.Equal(t, "1", renderTrivyOperator(t, "testbed", `select(.kind == "ConfigMap" and .metadata.name == "trivy-operator-config") | .data["OPERATOR_CONCURRENT_SCAN_JOBS_LIMIT"]`))
	assert.Equal(t, "2", renderTrivyOperator(t, "dev", `select(.kind == "ConfigMap" and .metadata.name == "trivy-operator-config") | .data["OPERATOR_CONCURRENT_SCAN_JOBS_LIMIT"]`))
	assert.NotEmpty(t, renderTrivyOperator(t, "dev", `select(.kind == "ConfigMap" and .metadata.name == "trivy-operator-trivy-config") | .data["trivy.resources.limits.memory"]`))
}

// idleReplicaSets drops the reports of ReplicaSets a rollout left at 0 replicas (kept as the Deployment's history)
const idleReplicaSets = `unless on (namespace, resource_name) label_replace(kube_replicaset_spec_replicas == 0, "resource_name", "$1", "replicaset", "(.+)")`
