package test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
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

// In the sandbox: the operator reports running images, an image with a known fixable CRITICAL CVE raises
// ImageCriticalVulnerability's expression, and no other workload has an unaccepted CRITICAL finding
func TestTrivyOperator(t *testing.T) {
	secretsSandboxOnly(t)
	operator := k8s.NewKubectlOptions("", "", "trivy-operator")
	k8s.WaitUntilDeploymentAvailable(t, operator, "trivy-operator", 60, 10*time.Second)

	reported := func(namespace string) bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, k8s.NewKubectlOptions("", "", namespace), "get", "vulnerabilityreports.aquasecurity.github.io",
			"-o", "jsonpath={.items[*].report.summary.criticalCount}")
		return err == nil && strings.TrimSpace(out) != ""
	}
	require.Eventually(t, func() bool { return reported("excalidraw") }, 45*time.Minute, 30*time.Second, "no vulnerability report for excalidraw")

	// alpine 3.14.0: four CRITICAL CVEs with a fix (CVE-2021-36159, CVE-2021-3711)
	namespace := fmt.Sprint("trivy-test-", time.Now().Unix())
	opts := k8s.NewKubectlOptions("", "", namespace)
	k8s.CreateNamespace(t, opts, namespace)
	defer k8s.DeleteNamespace(t, opts, namespace)
	k8s.RunKubectl(t, opts, "run", "old-alpine", "--image=docker.io/library/alpine:3.14.0", "--restart=Never", "--", "sleep", "3600")
	fixed := `count by (namespace, resource_kind, resource_name, image_repository) (trivy_vulnerability_id{severity="Critical", fixed_version!=""}) > 0`
	require.Eventually(t, func() bool {
		found, err := prometheusQuery(t, fixed)
		if err != nil {
			return false
		}
		for _, m := range found {
			if m["namespace"] == namespace {
				return true
			}
		}
		return false
	}, 30*time.Minute, 30*time.Second, "ImageCriticalVulnerability's expression does not see alpine 3.14.0 in %s", namespace)

	// The baseline: every other CRITICAL finding is fixed by a bump or accepted (system/trivy-operator/values.yaml)
	var open []string
	for _, expr := range []string{fixed, strings.Replace(fixed, `fixed_version!=""`, `fixed_version=""`, 1)} {
		found, err := prometheusQuery(t, expr)
		require.NoError(t, err)
		for _, m := range found {
			if m["namespace"] != namespace {
				open = append(open, fmt.Sprintf("%s/%s %s (%s)", m["namespace"], m["resource_name"], m["image_repository"], map[bool]string{true: "fix", false: "no fix"}[expr == fixed]))
			}
		}
	}
	assert.Empty(t, open, "CRITICAL findings neither fixed nor accepted")
}
