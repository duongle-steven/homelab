package test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/duongle-steven/homelab/test/internal/testenv"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// auditEntries waits up to 5 minutes for a LogQL query to return lines from the last `since`
func auditEntries(t *testing.T, query string, since time.Duration) []lokiEntry {
	t.Helper()
	var entries []lokiEntry
	require.Eventually(t, func() bool {
		var err error
		entries, err = lokiQuery(t, query, since)
		return err == nil && len(entries) > 0
	}, 5*time.Minute, 10*time.Second, "nothing for %s", query)
	return entries
}

// An exec into a pod is in the API server's audit log, with who did it
func TestSecurityLogsK3sExec(t *testing.T) {
	testenv.LogsSandboxOnly(t)
	options := logPod(t, "audit")
	printTo(t, options, "audit", fmt.Sprint("audit-exec-", time.Now().UnixNano()))
	entries := auditEntries(t, fmt.Sprintf(
		`{job="audit/k3s"} | json | objectRef_subresource="exec" | objectRef_namespace=%q | objectRef_name="audit"`, logsNamespace),
		15*time.Minute)
	var event struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
		Level string `json:"level"`
	}
	require.NoError(t, json.Unmarshal([]byte(entries[0].Line), &event))
	assert.Equal(t, "system:admin", event.User.Username, "the k3d kubeconfig's user")
	assert.Equal(t, "Metadata", event.Level)
	assert.NotEmpty(t, entries[0].Labels["node"])
}

// An OpenBao read is in its audit log, labelled apart from its server log, and the secret's value is not (OpenBao
// HMACs it)
func TestSecurityLogsOpenBao(t *testing.T) {
	testenv.LogsSandboxOnly(t)
	value := fmt.Sprint("audit-value-", time.Now().UnixNano())
	baoPutStdin(t, "smoke/audit-marker", "value", value)
	t.Cleanup(func() { baoPut(t, "smoke/audit-marker", "", true) })
	got, err := bao(t, "admin", "openbao", "openbao-admin", "kv", "get", "-field=value", "secret/smoke/audit-marker")
	require.NoError(t, err)
	require.Equal(t, value, got)

	entries := auditEntries(t, `{job="audit/openbao"} |= "secret/data/smoke/audit-marker" | json | type="response" | request_operation="read"`, 15*time.Minute)
	assert.Equal(t, "openbao", entries[0].Labels["namespace"])

	lines, err := lokiQuery(t, `{namespace="openbao"}`, 15*time.Minute)
	require.NoError(t, err)
	for _, l := range lines {
		require.NotContains(t, l.Line, value, "a secret's value in Loki (%s)", l.Labels["job"])
	}
	server, err := lokiQuery(t, `{job="pods", namespace="openbao"} |= "secret/data/smoke/audit-marker"`, 15*time.Minute)
	require.NoError(t, err)
	assert.Empty(t, server, "audit lines left under job=pods")
}

// activeAlerts reads Alertmanager's active alerts from inside its pod
func activeAlerts(t *testing.T) []map[string]string {
	t.Helper()
	out, err := k8s.RunKubectlAndGetOutputE(t, k8s.NewKubectlOptions("", "", "monitoring-system"), "exec",
		"alertmanager-monitoring-system-kube-pro-alertmanager-0", "-c", "alertmanager", "--",
		"wget", "-qO-", "http://localhost:9093/api/v2/alerts?active=true")
	require.NoError(t, err)
	var alerts []struct {
		Labels map[string]string `json:"labels"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &alerts))
	labels := make([]map[string]string, 0, len(alerts))
	for _, a := range alerts {
		labels = append(labels, a.Labels)
	}
	return labels
}

// An exec by a person raises AuditExecByPerson in Alertmanager, naming the user
func TestSecurityLogsExecAlert(t *testing.T) {
	testenv.LogsSandboxOnly(t)
	options := logPod(t, "audit-alert")
	printTo(t, options, "audit-alert", "audit-alert")
	require.Eventually(t, func() bool {
		for _, a := range activeAlerts(t) {
			if a["alertname"] == "AuditExecByPerson" && a["user"] == "system:admin" && a["object"] == "audit-alert" {
				return true
			}
		}
		return false
	}, 6*time.Minute, 20*time.Second, "no AuditExecByPerson alert for the exec")
}

// Service accounts never raise the person alerts, and both audit trails are flowing
func TestSecurityLogsQuietWhenIdle(t *testing.T) {
	testenv.LogsSandboxOnly(t)
	for _, a := range activeAlerts(t) {
		if a["source"] != "audit" {
			continue
		}
		assert.False(t, strings.HasPrefix(a["user"], "system:serviceaccount:"), "%s for %s", a["alertname"], a["user"])
		assert.NotEqual(t, "AuditLogsMissing", a["alertname"], "an audit trail stopped: %v", a)
	}
	// No critical rule matched anything in the last 2 hours, a fresh cluster's start included (OpenBao's
	// self-initialisation runs with a root token of its own): the repository's rules, evaluated by Loki over the log
	var parsed struct {
		Groups []struct {
			Rules []struct {
				Alert  string            `yaml:"alert"`
				Expr   string            `yaml:"expr"`
				Labels map[string]string `yaml:"labels"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(render.Loki(t, "", `select(.kind == "ConfigMap" and .metadata.name == "loki-audit-rules") | .data["audit-rules.yaml"]`)), &parsed))
	options := k8s.NewKubectlOptions("", "", "loki")
	options.Logger = logger.Discard
	checked := 0
	for _, g := range parsed.Groups {
		for _, r := range g.Rules {
			if r.Labels["severity"] != "critical" || r.Alert == "AuditLogsMissing" {
				continue
			}
			checked++
			params := url.Values{}
			params.Set("query", r.Expr)
			params.Set("start", fmt.Sprint(time.Now().Add(-2*time.Hour).Unix()))
			params.Set("end", fmt.Sprint(time.Now().Unix()))
			params.Set("step", "60")
			out, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "--raw",
				"/api/v1/namespaces/loki/services/loki:3100/proxy/loki/api/v1/query_range?"+params.Encode())
			require.NoError(t, err, r.Alert)
			var res struct {
				Data struct {
					Result []struct {
						Metric map[string]string `json:"metric"`
					} `json:"result"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal([]byte(out), &res), r.Alert)
			for _, m := range res.Data.Result {
				t.Errorf("%s would have fired: %v", r.Alert, m.Metric)
			}
		}
	}
	assert.NotZero(t, checked, "no critical audit rule")
}

// lokiLines returns every line a LogQL query matches over the last `since`, oldest first, paging past Loki's limit
// per query (the query never holds a value the caller looks for: the API server's audit log records its URL)
func lokiLines(t *testing.T, query string, since time.Duration) []string {
	t.Helper()
	options := k8s.NewKubectlOptions("", "", "loki")
	options.Logger = logger.Discard
	var lines []string
	start, end := time.Now().Add(-since).UnixNano(), time.Now().UnixNano()
	for len(lines) < 500000 {
		params := url.Values{}
		params.Set("query", query)
		params.Set("start", fmt.Sprint(start))
		params.Set("end", fmt.Sprint(end))
		params.Set("limit", "5000")
		params.Set("direction", "forward")
		out, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "--raw",
			"/api/v1/namespaces/loki/services/loki:3100/proxy/loki/api/v1/query_range?"+params.Encode())
		require.NoError(t, err)
		var res struct {
			Data struct {
				Result []struct {
					Values [][2]string `json:"values"`
				} `json:"result"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &res))
		n, last := 0, start
		for _, r := range res.Data.Result {
			for _, v := range r.Values {
				lines = append(lines, v[1])
				n++
				if ts, err := strconv.ParseInt(v[0], 10, 64); err == nil && ts > last {
					last = ts
				}
			}
		}
		if n < 5000 {
			return lines
		}
		start = last + 1
	}
	t.Fatalf("more than 500000 lines for %s", query)
	return nil
}

// No generated secret's value is in either audit log (a Secret's body is never logged, OpenBao HMACs values)
func TestSecurityLogsNoSecretValues(t *testing.T) {
	testenv.LogsSandboxOnly(t)
	// On a fresh cluster the suite may reach this before Argo CD synced the apps
	for _, ns := range []string{"grafana", "forgejo"} {
		k8s.WaitUntilSecretAvailable(t, k8s.NewKubectlOptions("", "", ns), ns+"-admin", 60, 10*time.Second)
	}
	values := map[string]string{
		"grafana/grafana-admin": secretValue(t, "grafana", "grafana-admin", "admin-password"),
		"forgejo/forgejo-admin": secretValue(t, "forgejo", "forgejo-admin", "password"),
		"ntfy alerts token":     ntfyToken(t, "alerts"),
	}
	lines := lokiLines(t, `{job=~"audit/.*"}`, 2*time.Hour)
	require.NotEmpty(t, lines, "no audit line in the last 2 hours")
	t.Logf("searched %d audit lines", len(lines))
	for name, value := range values {
		require.NotEmpty(t, value, name)
		for _, l := range lines {
			if strings.Contains(l, value) {
				t.Errorf("%s's value is in an audit line", name) // never print the line: it holds the value
				break
			}
		}
	}
}

// A change too big for one Loki line (a large CRD or Role) still reaches Loki: the API server truncates its body
func TestSecurityLogsLargeChange(t *testing.T) {
	testenv.LogsSandboxOnly(t)
	options := k8s.NewKubectlOptions("", "", logsNamespace)
	k8s.RunKubectl(t, options, "get", "namespace", logsNamespace)
	name := fmt.Sprint("audit-large-", time.Now().UnixNano())
	names := make([]string, 6000)
	for i := range names {
		names[i] = fmt.Sprintf("%s-resource-name-%04d", name, i) // about 300KB in all
	}
	role, err := json.Marshal(map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role",
		"metadata": map[string]string{"name": name, "namespace": logsNamespace},
		"rules":    []map[string]any{{"apiGroups": []string{""}, "resources": []string{"configmaps"}, "verbs": []string{"get"}, "resourceNames": names}},
	})
	require.NoError(t, err)
	require.Greater(t, len(role), 256*1024)
	cmd := exec.Command("kubectl", "create", "-f", "-")
	cmd.Stdin = strings.NewReader(string(role))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	defer k8s.RunKubectl(t, options, "delete", "role", name)

	entries := auditEntries(t, fmt.Sprintf(`{job="audit/k3s"} |= %q | json | objectRef_resource="roles" | verb="create"`, name), 15*time.Minute)
	var event struct {
		Level       string            `json:"level"`
		Annotations map[string]string `json:"annotations"`
	}
	require.NoError(t, json.Unmarshal([]byte(entries[0].Line), &event))
	assert.Equal(t, "Request", event.Level)
	assert.Equal(t, "true", event.Annotations["audit.k8s.io/truncated"], "the event kept its body")
}
