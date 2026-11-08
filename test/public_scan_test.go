package test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The weekly scan runs in the sandbox against the public apps it serves, through the LAN Gateway: it completes, loads
// its templates, finds nothing unaccepted at medium or above, and Loki's ruler holds PublicScanFinding
func TestPublicScan(t *testing.T) {
	sandboxOnly(t, "system/public-scan runs in the sandbox only")
	options := k8s.NewKubectlOptions("", "", "public-scan")
	options.Logger = logger.Discard
	job := fmt.Sprintf("public-scan-test-%d", time.Now().Unix())
	_, err := k8s.RunKubectlAndGetOutputE(t, options, "create", "job", job, "--from=cronjob/public-scan")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = k8s.RunKubectlAndGetOutputE(t, options, "delete", "job", job, "--ignore-not-found") })

	var state string
	require.Eventually(t, func() bool {
		state, _ = k8s.RunKubectlAndGetOutputE(t, options, "get", "job", job, "--output",
			`jsonpath={.status.conditions[?(@.status=="True")].type}`)
		return strings.Contains(state, "Complete") || strings.Contains(state, "Failed")
	}, 40*time.Minute, 20*time.Second, "the scan did not finish")
	logs, err := k8s.RunKubectlAndGetOutputE(t, options, "logs", "job/"+job, "--tail=-1")
	require.NoError(t, err)
	require.Contains(t, state, "Complete", "the scan failed: %s", scanTail([]byte(logs)))

	lines := strings.Split(strings.TrimSpace(logs), "\n")
	var done struct {
		PublicScan string `json:"public_scan"`
		Templates  int    `json:"templates"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &done), lines[len(lines)-1])
	assert.Equal(t, "done", done.PublicScan)
	out, err := exec.Command("yq", ".minTemplates", "../system/public-scan/values.yaml").Output()
	require.NoError(t, err)
	floor, err := strconv.Atoi(strings.TrimSpace(string(out)))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, done.Templates, floor)
	t.Logf("templates loaded: %d", done.Templates)
	for _, line := range lines {
		var f struct{ PublicScan, Template, Host, Severity string }
		if json.Unmarshal([]byte(line), &f) != nil || f.PublicScan != "finding" {
			continue
		}
		t.Logf("finding: %s on %s (%s)", f.Template, f.Host, f.Severity)
		assert.NotContains(t, []string{"medium", "high", "critical"}, f.Severity, "an unaccepted finding: %s on %s", f.Template, f.Host)
	}

	loki := k8s.NewKubectlOptions("", "", "loki")
	loki.Logger = logger.Discard
	require.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, loki, "get", "--raw",
			"/api/v1/namespaces/loki/services/loki:3100/proxy/prometheus/api/v1/rules")
		return err == nil && strings.Contains(out, `"name":"PublicScanFinding"`)
	}, 5*time.Minute, 15*time.Second, "Loki's ruler has no PublicScanFinding")
}
