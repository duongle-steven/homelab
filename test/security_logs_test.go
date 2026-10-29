package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
