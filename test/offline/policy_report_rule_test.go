package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A new fail or error in Kyverno's reports alerts once per namespace, top owner, policy and rule, and keeps firing 15
// minutes after a short-lived pod's report is gone (G1, docs/superpowers/specs/2026-11-27-policy-report-alert-design.md).
// promtool runs the rule on fixtures/policy-report-rule-tests.yaml
func TestPolicyReportRule(t *testing.T) {
	rule := `select(.kind == "PrometheusRule" and .metadata.name == "kyverno-policy-reports")`
	alert := func(expr string) string {
		return render.Yq(t, "system/kyverno", "", rule+` | .spec.groups[].rules[] | select(.alert == "PolicyReportFail") | `+expr)
	}
	assert.Equal(t, "15m", alert(".keep_firing_for"))
	assert.Equal(t, "null", alert(".for"))
	assert.Equal(t, "warning", alert(".labels.severity"))
	assert.Equal(t, "null", alert(".labels.source"), "routed like every warning: by namespace, muted at night")
	description := alert(".annotations.description")
	assert.Contains(t, description, "PolicyException")
	assert.Contains(t, description, "docs/how-to-guides/policy.md")

	dir := t.TempDir()
	spec := render.Yq(t, "system/kyverno", "", rule+` | .spec`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rules.yaml"), []byte(spec), 0o600))
	tests, err := os.ReadFile(filepath.Join(repo.Root(), "test/offline/fixtures/policy-report-rule-tests.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tests.yaml"), tests, 0o600))
	out, err := exec.Command("promtool", "test", "rules", filepath.Join(dir, "tests.yaml")).CombinedOutput()
	require.NoError(t, err, "promtool test rules:\n%s", out)

	// kube-state-metrics reads the reports, nothing more
	rbac := render.Yq(t, "system/monitoring-system", "",
		`select(.kind == "ClusterRole" and .metadata.name == "monitoring-system-kube-state-metrics") | .rules[] | select(.apiGroups[0] == "wgpolicyk8s.io") | (.resources | join(",")) + " " + (.verbs | join(","))`)
	assert.Equal(t, "policyreports,clusterpolicyreports list,watch", rbac)
}
