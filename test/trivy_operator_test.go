package test

import (
	"crypto/sha256"
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
	// The sandbox: one scan at a time, with limits
	assert.Equal(t, "1", renderTrivyOperator(t, "dev", `select(.kind == "ConfigMap" and .metadata.name == "trivy-operator-config") | .data["OPERATOR_CONCURRENT_SCAN_JOBS_LIMIT"]`))
	assert.NotEmpty(t, renderTrivyOperator(t, "dev", `select(.kind == "ConfigMap" and .metadata.name == "trivy-operator-trivy-config") | .data["trivy.resources.limits.memory"]`))
}

// idleReplicaSets drops the reports of ReplicaSets a rollout left at 0 replicas (kept as the Deployment's history)
const idleReplicaSets = `unless on (namespace, resource_name) label_replace(kube_replicaset_spec_replicas == 0, "resource_name", "$1", "replicaset", "(.+)")`

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

	// A run cut by go test's timeout skips its deferred cleanup: its old alpine would fail this test and TestPolicy
	leftovers, err := k8s.RunKubectlAndGetOutputE(t, operator, "get", "namespaces", "-o", "name")
	require.NoError(t, err)
	for _, ns := range strings.Fields(leftovers) {
		if strings.HasPrefix(ns, "namespace/trivy-test-") {
			_, _ = k8s.RunKubectlAndGetOutputE(t, operator, "delete", ns, "--wait=false")
		}
	}

	// alpine 3.14.0: four CRITICAL CVEs with a fix (CVE-2021-36159, CVE-2021-3711)
	namespace := fmt.Sprint("trivy-test-", time.Now().Unix())
	opts := k8s.NewKubectlOptions("", "", namespace)
	k8s.CreateNamespace(t, opts, namespace)
	defer k8s.DeleteNamespace(t, opts, namespace)
	k8s.RunKubectl(t, opts, "run", "old-alpine", "--image=docker.io/library/alpine:3.14.0", "--restart=Never", "--", "sleep", "3600")
	// The alerts' own expressions, as deployed
	get := func(args ...string) string {
		out, err := k8s.RunKubectlAndGetOutputE(t, operator, args...)
		require.NoError(t, err)
		return out
	}
	alert := func(name string) string {
		return get("get", "prometheusrules.monitoring.coreos.com", "trivy-operator", "-o", `jsonpath={.spec.groups[0].rules[?(@.alert=="`+name+`")].expr}`)
	}
	fixed, unfixed := alert("ImageCriticalVulnerability"), alert("ImageCriticalVulnerabilityUnfixed")
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

	// Every running workload scanned first: a clean baseline over half the workloads proves nothing.
	// The operator sometimes drops a workload it lists at start, silently (ImageScanMissing fires for it in 6 hours):
	// one still unscanned after 10 minutes is touched once, and its update event queues its scan. So an operator that
	// scans a workload only after an update passes here; production's alert still catches it
	missing := alert("ImageScanMissing")
	var unscanned []string
	since := map[string]time.Time{}
	touched := map[string]bool{}
	assert.Eventually(t, func() bool {
		found, err := prometheusQuery(t, missing)
		if err != nil {
			return false
		}
		unscanned = nil
		for _, m := range found {
			workload := m["namespace"] + "/" + m["resource_name"]
			unscanned = append(unscanned, workload)
			if since[workload].IsZero() {
				since[workload] = time.Now()
			}
			if touched[workload] || time.Since(since[workload]) < 10*time.Minute {
				continue
			}
			touched[workload] = true
			for _, kind := range []string{"replicaset", "statefulset", "daemonset"} {
				if m[kind] != "" {
					t.Logf("%s has had no report for 10 minutes: touching its %s", workload, kind)
					if out, err := k8s.RunKubectlAndGetOutputE(t, k8s.NewKubectlOptions("", "", m["namespace"]), "annotate", kind, m[kind],
						fmt.Sprint("homelab/trivy-rescan=", time.Now().Unix()), "--overwrite"); err != nil {
						t.Logf("touching %s failed: %v %s", workload, err, out)
					}
				}
			}
		}
		return len(unscanned) == 0
	}, 20*time.Minute, 30*time.Second, "running workloads with no vulnerability report")
	require.Empty(t, unscanned, "running workloads with no vulnerability report (ImageScanMissing's expression)")

	// The baseline: every other CRITICAL finding is fixed by a bump or accepted (system/trivy-operator/values.yaml).
	// The operator applies its ignore file only to new scans, and rescans a report only when it is a day old
	// (OPERATOR_SCANNER_REPORT_TTL): a report scanned before the operator's start is deleted, so it is scanned again
	// under the current settings
	started := get("get", "pods", "-l", "app.kubernetes.io/name=trivy-operator", "-o", "jsonpath={.items[0].status.startTime}")
	operatorStart, err := time.Parse(time.RFC3339, started)
	require.NoError(t, err)
	rescanned := map[string]bool{}
	deleted := map[string][2]string{} // workload: its namespace and report selector, until scanned again
	var settled time.Time             // a new report reaches Prometheus at the next scrape
	var open []string
	assert.Eventually(t, func() bool {
		open = nil
		for _, expr := range []string{fixed, unfixed} {
			found, err := prometheusQuery(t, expr)
			if err != nil {
				return false
			}
			for _, m := range found {
				if m["namespace"] == namespace {
					continue
				}
				workload := m["namespace"] + "/" + m["resource_name"]
				open = append(open, fmt.Sprintf("%s %s (%s)", workload, m["image_repository"], map[bool]string{true: "fix", false: "no fix"}[expr == fixed]))
				if rescanned[workload] {
					continue
				}
				rescanned[workload] = true
				reports := k8s.NewKubectlOptions("", "", m["namespace"])
				selector := "trivy-operator.resource.name=" + m["resource_name"]
				created, err := k8s.RunKubectlAndGetOutputE(t, reports, "get", "vulnerabilityreports.aquasecurity.github.io", "-l", selector,
					"-o", "jsonpath={.items[*].metadata.creationTimestamp}")
				if err != nil {
					continue
				}
				for _, c := range strings.Fields(created) {
					if at, err := time.Parse(time.RFC3339, c); err == nil && at.Before(operatorStart) {
						t.Logf("%s was scanned before the operator's start: scanning it again", workload)
						_, _ = k8s.RunKubectlAndGetOutputE(t, reports, "delete", "vulnerabilityreports.aquasecurity.github.io", "-l", selector)
						deleted[workload] = [2]string{m["namespace"], selector}
						break
					}
				}
			}
		}
		// A deleted report hides its findings until the new scan: wait for it
		for workload, d := range deleted {
			out, err := k8s.RunKubectlAndGetOutputE(t, k8s.NewKubectlOptions("", "", d[0]), "get", "vulnerabilityreports.aquasecurity.github.io",
				"-l", d[1], "-o", "jsonpath={.items[*].metadata.name}")
			if err != nil || strings.TrimSpace(out) == "" {
				open = append(open, workload+" (being scanned again)")
				continue
			}
			delete(deleted, workload)
			settled = time.Now().Add(2 * time.Minute)
		}
		return len(open) == 0 && time.Now().After(settled)
	}, 30*time.Minute, 30*time.Second, "CRITICAL findings neither fixed nor accepted")
	assert.Empty(t, open, "CRITICAL findings neither fixed nor accepted")
}
