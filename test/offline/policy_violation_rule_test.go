package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Kyverno's admission events alert from Loki, for pods gone before kube-state-metrics saw their report (G1,
// docs/superpowers/specs/2026-11-27-policy-report-alert-design.md): admission only (a scan repeats every known
// failure), never the copy on the policy itself, one alert per namespace, kind and policy, never a pod's name
func TestPolicyViolationRule(t *testing.T) {
	rules := render.Loki(t, "", `select(.kind == "ConfigMap" and .metadata.name == "loki-audit-rules") | .data["policy-rules.yaml"]`)
	require.NotEmpty(t, rules, "no policy-rules.yaml in ConfigMap loki-audit-rules")
	file := filepath.Join(t.TempDir(), "policy-rules.yaml")
	require.NoError(t, os.WriteFile(file, []byte(rules), 0o600))
	for _, cmd := range [][]string{{"rules", "lint", file}, {"rules", "check", file}} {
		out, err := exec.Command("lokitool", cmd...).CombinedOutput()
		require.NoError(t, err, "lokitool %s: %s", cmd[1], out)
	}
	var parsed struct {
		Groups []struct {
			Rules []struct {
				Alert       string            `yaml:"alert"`
				Expr        string            `yaml:"expr"`
				Labels      map[string]string `yaml:"labels"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(rules), &parsed))
	require.Len(t, parsed.Groups, 1)
	require.Len(t, parsed.Groups[0].Rules, 1)
	r := parsed.Groups[0].Rules[0]
	assert.Equal(t, "PolicyViolationEvent", r.Alert)
	assert.Equal(t, map[string]string{"severity": "warning", "source": "kyverno"}, r.Labels)
	assert.Contains(t, r.Expr, `sum by (namespace, kind, policy) (`)
	assert.Contains(t, r.Expr, `{service_name="kubernetes-events"}`)
	assert.Contains(t, r.Expr, `reason="PolicyViolation"`)
	assert.Contains(t, r.Expr, `component="kyverno-admission"`)
	assert.Contains(t, r.Expr, `kind!="ValidatingPolicy"`)
	assert.Contains(t, r.Annotations["description"], "PolicyException")
	assert.Contains(t, r.Annotations["description"], "docs/how-to-guides/policy.md")
}

// A known failure the reports already alert on (PolicyReportFail, silenced or not) keeps its admission events quiet:
// otherwise every new pod of a known-failing workload (a CronJob's runs, a rollout) would notify again
func TestPolicyViolationEventInhibited(t *testing.T) {
	const config = `select(.kind == "Secret" and .metadata.name == "alertmanager-monitoring-system-kube-pro-alertmanager") | .data."alertmanager.yaml" | @base64d | from_yaml`
	for _, env := range []string{"", "dev"} {
		rule := render.Yq(t, "system/monitoring-system", env, config+` | .inhibit_rules[] | select(.target_matchers[] == "alertname=\"PolicyViolationEvent\"") | (.source_matchers | join(",")) + " " + (.equal | join(","))`)
		assert.Equal(t, `alertname="PolicyReportFail" namespace,policy`, rule, "env %q", env)
	}
}
