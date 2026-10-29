package test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// docs/superpowers/specs/2026-10-28-security-logs-design.md

// The audit policy k3d and metal/roles/k3s pass to the API server
const auditPolicyPath = "metal/roles/k3s/files/audit-policy.yaml"

// yqFile runs a yq expression on a file of the repository
func yqFile(t *testing.T, file, expr string) string {
	t.Helper()
	cmd := exec.Command("yq", "-N", expr, file)
	cmd.Dir = ".."
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// The audit flags, as k3s takes them in --kube-apiserver-arg
var auditFlags = []string{
	"audit-policy-file=/etc/rancher/k3s/audit-policy.yaml",
	"audit-log-path=/var/log/kubernetes/audit/audit.log",
	"audit-log-maxsize=100",
	"audit-log-maxbackup=3",
}

type auditRule struct {
	Level     string   `yaml:"level"`
	Verbs     []string `yaml:"verbs"`
	Resources []struct {
		Group     string   `yaml:"group"`
		Resources []string `yaml:"resources"`
	} `yaml:"resources"`
	NonResourceURLs []string `yaml:"nonResourceURLs"`
}

// matchesEveryResource: a rule with neither resources nor nonResourceURLs applies to every request
func (r auditRule) matchesEveryResource() bool {
	return len(r.Resources) == 0 && len(r.NonResourceURLs) == 0
}

func (r auditRule) names(resource string) bool {
	for _, g := range r.Resources {
		if slices.Contains(g.Resources, resource) {
			return true
		}
	}
	return false
}

// No secret value ever reaches the log: sensitive resources are logged at Metadata before any rule could log their body
func TestAuditPolicyRender(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", auditPolicyPath))
	require.NoError(t, err)
	var policy struct {
		APIVersion string      `yaml:"apiVersion"`
		Kind       string      `yaml:"kind"`
		OmitStages []string    `yaml:"omitStages"`
		Rules      []auditRule `yaml:"rules"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &policy))
	assert.Equal(t, "audit.k8s.io/v1", policy.APIVersion)
	assert.Equal(t, "Policy", policy.Kind)
	assert.Contains(t, policy.OmitStages, "RequestReceived")
	for _, r := range policy.Rules {
		assert.NotEqual(t, "RequestResponse", r.Level, "no rule logs response bodies")
	}

	for _, resource := range []string{"secrets", "configmaps", "tokenreviews", "subjectaccessreviews",
		"serviceaccounts/token", "pods/exec", "pods/attach", "pods/portforward"} {
		first := slices.IndexFunc(policy.Rules, func(r auditRule) bool { return r.names(resource) })
		require.GreaterOrEqual(t, first, 0, "no rule names %s", resource)
		assert.Equal(t, "Metadata", policy.Rules[first].Level, "the first rule naming %s", resource)
		for i, r := range policy.Rules[:first] {
			assert.False(t, r.Level != "None" && r.matchesEveryResource(),
				"rule %d logs %s at %s before its own rule", i, resource, r.Level)
		}
	}

	var groups, resources, verbs []string
	for _, r := range policy.Rules {
		if r.Level != "Request" {
			continue
		}
		verbs = append(verbs, r.Verbs...)
		for _, g := range r.Resources {
			groups = append(groups, g.Group)
			resources = append(resources, g.Resources...)
		}
	}
	for _, g := range []string{"rbac.authorization.k8s.io", "policies.kyverno.io", "admissionregistration.k8s.io",
		"apiextensions.k8s.io"} {
		assert.Contains(t, groups, g, "the Request rules' groups")
	}
	// Every kind of Kyverno's groups (PolicyExceptions and the policies): the groups without a resource list
	for _, r := range policy.Rules {
		for _, g := range r.Resources {
			if r.Level == "Request" && strings.Contains(g.Group, "kyverno.io") {
				assert.Empty(t, g.Resources, "%s: every kind", g.Group)
			}
		}
	}
	for _, r := range []string{"roles", "clusterroles", "rolebindings", "clusterrolebindings",
		"validatingwebhookconfigurations", "mutatingwebhookconfigurations", "namespaces", "customresourcedefinitions"} {
		assert.Contains(t, resources, r, "the Request rules' resources")
	}
	slices.Sort(verbs)
	assert.Equal(t, []string{"create", "delete", "patch", "update"}, slices.Compact(verbs), "the Request rules' verbs")

	require.NotEmpty(t, policy.Rules)
	last := policy.Rules[len(policy.Rules)-1]
	assert.Equal(t, "Metadata", last.Level)
	assert.True(t, last.matchesEveryResource() && len(last.Verbs) == 0, "the last rule matches everything")

	k3d := yqFile(t, "infra/_modules/k3d/k3d.yaml", `[.options.k3s.extraArgs[].arg] | join(" ")`)
	metal := yqFile(t, "metal/roles/k3s/defaults/main.yml", `.k3s_server_config.kube-apiserver-arg | join(" ")`)
	for _, f := range auditFlags {
		assert.Contains(t, k3d, "--kube-apiserver-arg="+f, "k3d.yaml")
		assert.Contains(t, metal, f, "metal/roles/k3s")
	}
}

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
	logsSandboxOnly(t)
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
	logsSandboxOnly(t)
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

// renderLoki renders system/loki for env ("" or dev) and runs a yq expression on it
func renderLoki(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart system/loki $1 | yq -N "$0"`, expr, env)
	cmd.Dir = ".."
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// lokiConfig selects Loki's own config in the render
const lokiConfig = `select(.kind == "ConfigMap" and .metadata.name == "loki") | .data["config.yaml"] | from_yaml`

// Audit streams are kept longer than other logs in production; the sandbox keeps 7 days for everything
func TestSecurityLogsRetention(t *testing.T) {
	for env, want := range map[string][2]string{"": {"720h", "2160h"}, "dev": {"168h", "168h"}} {
		t.Run("env="+env, func(t *testing.T) {
			assert.Equal(t, want[0], renderLoki(t, env, lokiConfig+` | .limits_config.retention_period`))
			assert.Equal(t, `{job=~"audit/.*"} `+want[1], renderLoki(t, env, lokiConfig+
				` | .limits_config.retention_stream | map(.selector + " " + .period) | join(",")`))
		})
	}
}

// The alerts from logs (the spec's table) and their severities
var auditAlerts = map[string]string{
	"AuditExecByPerson":           "warning",
	"AuditSecretReadByPerson":     "warning",
	"AuditClusterAdminBinding":    "critical",
	"AuditAnonymousAllowed":       "critical",
	"AuditManyDenied":             "warning",
	"AuditPolicyExceptionChanged": "warning",
	"OpenBaoRootToken":            "critical",
	"OpenBaoManyFailedLogins":     "warning",
	"OpenBaoConfigChanged":        "critical",
	"AuditLogsMissing":            "critical",
}

// Who is not a person: service accounts, nodes and the control plane (system:admin, k3s's admin, is a person)
const notPerson = `system:serviceaccount:.*|system:node:.*|system:kube-.*|system:apiserver`

// Loki's ruler loads the alert rules (valid LogQL, lokitool) and sends them to Alertmanager, which groups the audit
// alerts by user
func TestSecurityLogsRules(t *testing.T) {
	rules := renderLoki(t, "", `select(.kind == "ConfigMap" and .metadata.name == "loki-audit-rules") | .data["audit-rules.yaml"]`)
	require.NotEmpty(t, rules, "no ConfigMap loki-audit-rules")
	file := filepath.Join(t.TempDir(), "audit-rules.yaml")
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
	seen := map[string]bool{}
	for _, g := range parsed.Groups {
		for _, r := range g.Rules {
			seen[r.Alert] = true
			assert.Equal(t, auditAlerts[r.Alert], r.Labels["severity"], "%s's severity", r.Alert)
			assert.Equal(t, "audit", r.Labels["source"], r.Alert)
			assert.NotEmpty(t, r.Annotations["summary"], r.Alert)
			switch r.Alert {
			case "AuditExecByPerson", "AuditSecretReadByPerson":
				assert.Contains(t, r.Expr, notPerson, "%s leaves out who is not a person", r.Alert)
			case "AuditClusterAdminBinding", "AuditPolicyExceptionChanged":
				assert.Contains(t, r.Expr, "system:serviceaccount:argocd:", "%s leaves out Argo CD", r.Alert)
			}
		}
	}
	for name := range auditAlerts {
		assert.True(t, seen[name], "no rule %s", name)
	}
	assert.Equal(t, "local /etc/loki/rules http://monitoring-system-kube-pro-alertmanager.monitoring-system:9093 true",
		renderLoki(t, "", lokiConfig+` | .ruler | .storage.type + " " + .storage.local.directory + " " + .alertmanager_url + " " + (.enable_api | tostring)`))
	assert.Equal(t, "/etc/loki/rules/fake", renderLoki(t, "",
		`select(.kind == "StatefulSet" and .metadata.name == "loki") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "audit-rules") | .mountPath`))

	const amConfig = `select(.kind == "Secret" and .metadata.name == "alertmanager-monitoring-system-kube-pro-alertmanager") | .data."alertmanager.yaml" | @base64d | from_yaml`
	assert.Equal(t, `ntfy alertname,user source="audit"`, renderVelero(t, "system/monitoring-system", "", amConfig+
		` | .route.routes[] | select(.matchers[] == "source=\"audit\"") | .receiver + " " + (.group_by | join(",")) + " " + (.matchers | join(","))`))
	assert.Equal(t, "9093", renderVelero(t, "system/monitoring-system", "",
		`select(.kind == "AuthorizationPolicy" and .metadata.name == "allow") | .spec.rules[] | select(.from[].source.principals[]? == "cluster.local/ns/loki/sa/loki") | .to[].operation.ports | join(",")`), "Loki's ruler may call Alertmanager")
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

// Loki's ruler loaded every alert rule, and each evaluates
func TestSecurityLogsRulesLoaded(t *testing.T) {
	logsSandboxOnly(t)
	options := k8s.NewKubectlOptions("", "", "loki")
	options.Logger = logger.Discard
	require.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "--raw",
			"/api/v1/namespaces/loki/services/loki:3100/proxy/prometheus/api/v1/rules")
		if err != nil {
			return false
		}
		var res struct {
			Data struct {
				Groups []struct {
					Rules []struct {
						Name   string `json:"name"`
						Health string `json:"health"`
					} `json:"rules"`
				} `json:"groups"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(out), &res) != nil {
			return false
		}
		healthy := map[string]bool{}
		for _, g := range res.Data.Groups {
			for _, r := range g.Rules {
				healthy[r.Name] = r.Health == "ok"
			}
		}
		for name := range auditAlerts {
			if !healthy[name] {
				t.Logf("%s: not loaded or not ok yet", name)
				return false
			}
		}
		return true
	}, 10*time.Minute, 30*time.Second, "Loki's ruler does not evaluate every audit rule")
}

// An exec by a person raises AuditExecByPerson in Alertmanager, naming the user
func TestSecurityLogsExecAlert(t *testing.T) {
	logsSandboxOnly(t)
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
	logsSandboxOnly(t)
	for _, a := range activeAlerts(t) {
		if a["source"] != "audit" {
			continue
		}
		assert.False(t, strings.HasPrefix(a["user"], "system:serviceaccount:"), "%s for %s", a["alertname"], a["user"])
		assert.NotEqual(t, "AuditLogsMissing", a["alertname"], "an audit trail stopped: %v", a)
	}
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
	logsSandboxOnly(t)
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
