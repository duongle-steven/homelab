package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/duongle-steven/homelab/test/internal/securitylogs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// docs/superpowers/specs/2026-10-28-security-logs-design.md

// The audit policy k3d and the nodes (infra/_modules/metal) pass to the API server
const auditPolicyPath = "infra/_modules/metal/flake/audit-policy.yaml"

// yqFile runs a yq expression on a file of the repository
func yqFile(t *testing.T, file, expr string) string {
	t.Helper()
	cmd := exec.Command("yq", "-N", expr, file)
	cmd.Dir = repo.Root()
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
	// An event over 100KiB loses its bodies, keeping who and what: Loki refuses a line over 256KB (max_line_size),
	// and a CRD's body reaches 1.5MB
	"audit-log-truncate-enabled=true",
	"audit-log-truncate-max-event-size=102400",
}

type auditRule struct {
	Level      string   `yaml:"level"`
	Users      []string `yaml:"users"`
	UserGroups []string `yaml:"userGroups"`
	Verbs      []string `yaml:"verbs"`
	Resources  []struct {
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

// level is the level the first matching rule gives a resource request, as the API server picks it
func level(rules []auditRule, user string, groups []string, verb, group, resource string) string {
	for _, r := range rules {
		if len(r.NonResourceURLs) > 0 ||
			len(r.Users) > 0 && !slices.Contains(r.Users, user) ||
			len(r.UserGroups) > 0 && !slices.ContainsFunc(r.UserGroups, func(g string) bool { return slices.Contains(groups, g) }) ||
			len(r.Verbs) > 0 && !slices.Contains(r.Verbs, verb) {
			continue
		}
		if len(r.Resources) == 0 || slices.ContainsFunc(r.Resources, func(g struct {
			Group     string   `yaml:"group"`
			Resources []string `yaml:"resources"`
		}) bool {
			return g.Group == group && (len(g.Resources) == 0 || slices.Contains(g.Resources, resource))
		}) {
			return r.Level
		}
	}
	return "None"
}

// No secret value ever reaches the log: sensitive resources are logged at Metadata before any rule could log their body
func TestAuditPolicyRender(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repo.Root(), auditPolicyPath))
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

	// The volume (the sandbox, 2026-10-29): service accounts' reads were 41% of it and Kyverno rewriting its own
	// webhooks 7%; what they change, their reads of the sensitive kinds and everyone else's reads stay
	sa, sas := "system:serviceaccount:argocd:argocd-application-controller", []string{"system:serviceaccounts", "system:authenticated"}
	kyverno := "system:serviceaccount:kyverno:kyverno-admission-controller"
	admin := []string{"system:masters", "system:authenticated"}
	for _, c := range []struct {
		user           string
		groups         []string
		verb, group, r string
		want           string
	}{
		{sa, sas, "get", "", "pods", "None"},
		{sa, sas, "watch", "apps", "deployments", "None"},
		{sa, sas, "get", "", "secrets", "Metadata"},
		{sa, sas, "list", "", "configmaps", "Metadata"},
		{sa, sas, "patch", "", "pods", "Metadata"},
		{sa, sas, "create", "rbac.authorization.k8s.io", "rolebindings", "Request"},
		{sa, sas, "update", "admissionregistration.k8s.io", "validatingwebhookconfigurations", "Request"},
		{kyverno, sas, "update", "admissionregistration.k8s.io", "validatingwebhookconfigurations", "Metadata"},
		{kyverno, sas, "patch", "admissionregistration.k8s.io", "mutatingwebhookconfigurations", "Metadata"},
		{kyverno, sas, "update", "policies.kyverno.io", "policyexceptions", "Request"},
		{"system:admin", admin, "get", "", "pods", "Metadata"},
		{"system:admin", admin, "delete", "", "namespaces", "Request"},
	} {
		assert.Equal(t, c.want, level(policy.Rules, c.user, c.groups, c.verb, c.group, c.r), "%s %s %s/%s", c.user, c.verb, c.group, c.r)
	}

	k3d := yqFile(t, "infra/_modules/k3d/k3d.yaml", `[.options.k3s.extraArgs[].arg] | join(" ")`)
	// The nodes' flags: TestMetalK3sFlags
	for _, f := range auditFlags {
		assert.Contains(t, k3d, "--kube-apiserver-arg="+f, "k3d.yaml")
	}
}

// lokiConfig selects Loki's own config in the render
const lokiConfig = `select(.kind == "ConfigMap" and .metadata.name == "loki") | .data["config.yaml"] | from_yaml`

// Audit streams are kept longer than other logs in production; the sandbox keeps 7 days for everything
func TestSecurityLogsRetention(t *testing.T) {
	for env, want := range map[string][2]string{"": {"720h", "1440h"}, "dev": {"168h", "168h"}} {
		t.Run("env="+env, func(t *testing.T) {
			assert.Equal(t, want[0], render.Loki(t, env, lokiConfig+` | .limits_config.retention_period`))
			assert.Equal(t, `{job=~"audit/.*"} `+want[1], render.Loki(t, env, lokiConfig+
				` | .limits_config.retention_stream | map(.selector + " " + .period) | join(",")`))
			// A query may span everything kept (Loki refuses longer than 721h by default)
			assert.Equal(t, "1441h", render.Loki(t, env, lokiConfig+` | .limits_config.max_query_length`))
		})
	}
}

// Who is not a person: service accounts, nodes and the control plane, k3s's own controllers among them (the sandbox's
// audit log, 2026-10-29); system:admin, k3s's admin, and anyone signing in are
var (
	notPersons = []string{"system:serviceaccount:argocd:argocd-application-controller", "system:node:k3d-homelab-dev-server-0", "system:kube-scheduler",
		"system:apiserver", "system:k3s-supervisor", "system:k3s-controller", "k3s-cloud-controller-manager"}
	persons = []string{"system:admin", "someone@idm.example"}
)

// personFilter is the user!~ regexp of a rule's LogQL, personAlso the users it names as persons anyway (or user="...")
var (
	personFilter = regexp.MustCompile(`user!~"([^"]*)"`)
	personAlso   = regexp.MustCompile(`or user="([^"]*)"`)
)

// argocdServer execs only for a person: Argo CD's web terminal (exec.enabled), never on its own behalf
const argocdServer = "system:serviceaccount:argocd:argocd-server"

// Loki's ruler loads the alert rules (valid LogQL, lokitool) and sends them to Alertmanager, which groups the audit
// alerts by user
func TestSecurityLogsRules(t *testing.T) {
	rules := render.Loki(t, "", `select(.kind == "ConfigMap" and .metadata.name == "loki-audit-rules") | .data["audit-rules.yaml"]`)
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
				For         string            `yaml:"for"`
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
			assert.Equal(t, securitylogs.AuditAlerts[r.Alert], r.Labels["severity"], "%s's severity", r.Alert)
			assert.Equal(t, "audit", r.Labels["source"], r.Alert)
			assert.NotEmpty(t, r.Annotations["summary"], r.Alert)
			switch r.Alert {
			case "AuditExecByPerson", "AuditSecretReadByPerson":
				m := personFilter.FindStringSubmatch(r.Expr)
				require.NotNil(t, m, "%s has no user!~ filter", r.Alert)
				filter := regexp.MustCompile("^(?:" + m[1] + ")$")
				person := func(u string) bool {
					for _, also := range personAlso.FindAllStringSubmatch(r.Expr, -1) {
						if also[1] == u {
							return true
						}
					}
					return !filter.MatchString(u)
				}
				for _, u := range notPersons {
					assert.False(t, person(u), "%s takes %s for a person", r.Alert, u)
				}
				for _, u := range persons {
					assert.True(t, person(u), "%s leaves out %s", r.Alert, u)
				}
				assert.Equal(t, r.Alert == "AuditExecByPerson", person(argocdServer), "%s and Argo CD's web terminal", r.Alert)
			case "AuditManyDenied":
				// A node's pods retry with their old tokens for about 5 minutes after a power cut (istio-cni: 238 in 5m)
				assert.Equal(t, "10m", r.For, "%s fires on a power cut's stale tokens", r.Alert)
			case "AuditClusterAdminBinding", "AuditPolicyExceptionChanged":
				assert.Contains(t, r.Expr, "system:serviceaccount:argocd:", "%s leaves out Argo CD", r.Alert)
			}
		}
	}
	for name := range securitylogs.AuditAlerts {
		assert.True(t, seen[name], "no rule %s", name)
	}
	assert.Equal(t, "local /etc/loki/rules http://monitoring-system-kube-pro-alertmanager.monitoring-system:9093 true",
		render.Loki(t, "", lokiConfig+` | .ruler | .storage.type + " " + .storage.local.directory + " " + .alertmanager_url + " " + (.enable_api | tostring)`))
	assert.Equal(t, "/etc/loki/rules/fake", render.Loki(t, "",
		`select(.kind == "StatefulSet" and .metadata.name == "loki") | .spec.template.spec.containers[0].volumeMounts[] | select(.name == "audit-rules") | .mountPath`))

	const amConfig = `select(.kind == "Secret" and .metadata.name == "alertmanager-monitoring-system-kube-pro-alertmanager") | .data."alertmanager.yaml" | @base64d | from_yaml`
	assert.Equal(t, `ntfy alertname,user source="audit"`, render.Yq(t, "system/monitoring-system", "", amConfig+
		` | .route.routes[] | select(.matchers[] == "source=\"audit\"") | .receiver + " " + (.group_by | join(",")) + " " + (.matchers | join(","))`))
	assert.Equal(t, "9093", render.Yq(t, "system/monitoring-system", "",
		`select(.kind == "AuthorizationPolicy" and .metadata.name == "allow") | .spec.rules[] | select(.from[].source.principals[]? == "cluster.local/ns/loki/sa/loki") | .to[].operation.ports | join(",")`), "Loki's ruler may call Alertmanager")
}
