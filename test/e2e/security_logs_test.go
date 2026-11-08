package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/duongle-steven/homelab/test/internal/securitylogs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// auditEntries waits up to 5 minutes for a LogQL query to return lines from the last `since`
func auditEntries(ctx context.Context, query string, since time.Duration) []lokiEntry {
	GinkgoHelper()
	var entries []lokiEntry
	Eventually(ctx, func() bool {
		var err error
		entries, err = lokiQuery(ctx, query, since)
		return err == nil && len(entries) > 0
	}).WithTimeout(5*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "nothing for %s", query)
	return entries
}

// An exec into a pod is in the API server's audit log, with who did it
var _ = Describe("SecurityLogsK3sExec", Label("sandbox", "testbed", "security-logs", "logs"), func() {
	It("logs an exec with its user", func(ctx SpecContext) {
		logPod(ctx, "audit")
		printTo(ctx, "audit", fmt.Sprint("audit-exec-", time.Now().UnixNano()))
		entries := auditEntries(ctx, fmt.Sprintf(
			`{job="audit/k3s"} | json | objectRef_subresource="exec" | objectRef_namespace=%q | objectRef_name="audit"`, logsNamespace),
			15*time.Minute)
		var event struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
			Level string `json:"level"`
		}
		Expect(json.Unmarshal([]byte(entries[0].Line), &event)).To(Succeed())
		Expect(event.User.Username).To(Equal("system:admin"), "the k3d kubeconfig's user")
		Expect(event.Level).To(Equal("Metadata"))
		Expect(entries[0].Labels["node"]).NotTo(BeEmpty())
	}, SpecTimeout(15*time.Minute))
})

// An OpenBao read is in its audit log, labelled apart from its server log, and the secret's value is not (OpenBao
// HMACs it)
var _ = Describe("SecurityLogsOpenBao", Label("sandbox", "testbed", "security-logs", "logs"), func() {
	It("logs a read without its value", func(ctx SpecContext) {
		value := fmt.Sprint("audit-value-", time.Now().UnixNano())
		baoPutStdin(ctx, "smoke/audit-marker", "value", value)
		DeferCleanup(func(ctx SpecContext) { baoPut(ctx, "smoke/audit-marker", "", true) }, NodeTimeout(5*time.Minute))
		got, err := bao(ctx, "admin", "openbao", "openbao-admin", "kv", "get", "-field=value", "secret/smoke/audit-marker")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(value))

		entries := auditEntries(ctx, `{job="audit/openbao"} |= "secret/data/smoke/audit-marker" | json | type="response" | request_operation="read"`, 15*time.Minute)
		Expect(entries[0].Labels["namespace"]).To(Equal("openbao"))

		lines, err := lokiQuery(ctx, `{namespace="openbao"}`, 15*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		for _, l := range lines {
			Expect(l.Line).NotTo(ContainSubstring(value), "a secret's value in Loki (%s)", l.Labels["job"])
		}
		server, err := lokiQuery(ctx, `{job="pods", namespace="openbao"} |= "secret/data/smoke/audit-marker"`, 15*time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(server).To(BeEmpty(), "audit lines left under job=pods")
	}, SpecTimeout(15*time.Minute))
})

// activeAlerts reads Alertmanager's active alerts from inside its pod
func activeAlerts(ctx context.Context) []map[string]string {
	GinkgoHelper()
	out, err := kube.Kubectl(ctx, "monitoring-system", "exec",
		"alertmanager-monitoring-system-kube-pro-alertmanager-0", "-c", "alertmanager", "--",
		"wget", "-qO-", "http://localhost:9093/api/v2/alerts?active=true")
	Expect(err).NotTo(HaveOccurred(), out)
	var alerts []struct {
		Labels map[string]string `json:"labels"`
	}
	Expect(json.Unmarshal([]byte(out), &alerts)).To(Succeed())
	labels := make([]map[string]string, 0, len(alerts))
	for _, a := range alerts {
		labels = append(labels, a.Labels)
	}
	return labels
}

// An exec by a person raises AuditExecByPerson in Alertmanager, naming the user
var _ = Describe("SecurityLogsExecAlert", Label("sandbox", "testbed", "security-logs", "logs"), func() {
	It("raises AuditExecByPerson", func(ctx SpecContext) {
		logPod(ctx, "audit-alert")
		printTo(ctx, "audit-alert", "audit-alert")
		Eventually(ctx, func() bool {
			for _, a := range activeAlerts(ctx) {
				if a["alertname"] == "AuditExecByPerson" && a["user"] == "system:admin" && a["object"] == "audit-alert" {
					return true
				}
			}
			return false
		}).WithTimeout(6*time.Minute).WithPolling(20*time.Second).Should(BeTrue(), "no AuditExecByPerson alert for the exec")
	}, SpecTimeout(15*time.Minute))
})

// Service accounts never raise the person alerts, and both audit trails are flowing
var _ = Describe("SecurityLogsQuietWhenIdle", Label("sandbox", "testbed", "security-logs", "logs"), func() {
	It("raises no person alert for service accounts", func(ctx SpecContext) {
		for _, a := range activeAlerts(ctx) {
			if a["source"] != "audit" {
				continue
			}
			Expect(strings.HasPrefix(a["user"], "system:serviceaccount:")).To(BeFalse(), "%s for %s", a["alertname"], a["user"])
			Expect(a["alertname"]).NotTo(Equal("AuditLogsMissing"), "an audit trail stopped: %v", a)
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
		Expect(yaml.Unmarshal([]byte(render.Loki(GinkgoTB(), "", `select(.kind == "ConfigMap" and .metadata.name == "loki-audit-rules") | .data["audit-rules.yaml"]`)), &parsed)).To(Succeed())
		checked := 0
		var fired []string
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
				out, err := kube.Kubectl(ctx, "loki", "get", "--raw",
					"/api/v1/namespaces/loki/services/loki:3100/proxy/loki/api/v1/query_range?"+params.Encode())
				Expect(err).NotTo(HaveOccurred(), r.Alert)
				var res struct {
					Data struct {
						Result []struct {
							Metric map[string]string `json:"metric"`
						} `json:"result"`
					} `json:"data"`
				}
				Expect(json.Unmarshal([]byte(out), &res)).To(Succeed(), r.Alert)
				for _, m := range res.Data.Result {
					fired = append(fired, fmt.Sprintf("%s would have fired: %v", r.Alert, m.Metric))
				}
			}
		}
		Expect(checked).NotTo(BeZero(), "no critical audit rule")
		Expect(fired).To(BeEmpty())
	}, SpecTimeout(10*time.Minute))
})

// lokiLines returns every line a LogQL query matches over the last `since`, oldest first, paging past Loki's limit
// per query (the query never holds a value the caller looks for: the API server's audit log records its URL)
func lokiLines(ctx context.Context, query string, since time.Duration) []string {
	GinkgoHelper()
	var lines []string
	start, end := time.Now().Add(-since).UnixNano(), time.Now().UnixNano()
	for len(lines) < 500000 {
		params := url.Values{}
		params.Set("query", query)
		params.Set("start", fmt.Sprint(start))
		params.Set("end", fmt.Sprint(end))
		params.Set("limit", "5000")
		params.Set("direction", "forward")
		out, err := kube.Kubectl(ctx, "loki", "get", "--raw",
			"/api/v1/namespaces/loki/services/loki:3100/proxy/loki/api/v1/query_range?"+params.Encode())
		Expect(err).NotTo(HaveOccurred())
		var res struct {
			Data struct {
				Result []struct {
					Values [][2]string `json:"values"`
				} `json:"result"`
			} `json:"data"`
		}
		Expect(json.Unmarshal([]byte(out), &res)).To(Succeed())
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
	Fail(fmt.Sprintf("more than 500000 lines for %s", query))
	return nil
}

// No generated secret's value is in either audit log (a Secret's body is never logged, OpenBao HMACs values)
var _ = Describe("SecurityLogsNoSecretValues", Label("sandbox", "testbed", "security-logs", "logs"), func() {
	It("keeps generated values out of the audit logs", func(ctx SpecContext) {
		// On a fresh cluster the suite may reach this before Argo CD synced the apps (terratest's 60 tries 10 s apart)
		for _, ns := range []string{"grafana", "forgejo"} {
			Eventually(ctx, func() error {
				_, err := kube.Kubectl(ctx, ns, "get", "secret", ns+"-admin", "-o", "name")
				return err
			}).WithTimeout(10*time.Minute).WithPolling(10*time.Second).Should(Succeed(), "Secret %s/%s-admin", ns, ns)
		}
		values := map[string]string{
			"grafana/grafana-admin": secretValue(ctx, "grafana", "grafana-admin", "admin-password"),
			"forgejo/forgejo-admin": secretValue(ctx, "forgejo", "forgejo-admin", "password"),
			"ntfy alerts token":     ntfyToken(ctx, "alerts"),
		}
		lines := lokiLines(ctx, `{job=~"audit/.*"}`, 2*time.Hour)
		Expect(lines).NotTo(BeEmpty(), "no audit line in the last 2 hours")
		GinkgoWriter.Printf("searched %d audit lines\n", len(lines))
		var leaked []string
		for name, value := range values {
			Expect(value).NotTo(BeEmpty(), name)
			for _, l := range lines {
				if strings.Contains(l, value) {
					leaked = append(leaked, name) // never print the line: it holds the value
					break
				}
			}
		}
		Expect(leaked).To(BeEmpty(), "values in an audit line")
	}, SpecTimeout(40*time.Minute))
})

// A change too big for one Loki line (a large CRD or Role) still reaches Loki: the API server truncates its body
var _ = Describe("SecurityLogsLargeChange", Label("sandbox", "testbed", "security-logs", "logs"), func() {
	It("logs a truncated large change", func(ctx SpecContext) {
		// The logs specs share logsNamespace and run in any order: the first creates it
		_, _ = kube.Kubectl(ctx, "", "create", "namespace", logsNamespace)
		_, err := kube.Kubectl(ctx, "", "get", "namespace", logsNamespace)
		Expect(err).NotTo(HaveOccurred())
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
		Expect(err).NotTo(HaveOccurred())
		Expect(len(role)).To(BeNumerically(">", 256*1024))
		cmd := exec.CommandContext(ctx, "kubectl", "create", "-f", "-")
		cmd.Stdin = strings.NewReader(string(role))
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "%s", out)
		DeferCleanup(func(ctx SpecContext) {
			_, err := kube.Kubectl(ctx, logsNamespace, "delete", "role", name)
			Expect(err).NotTo(HaveOccurred())
		}, NodeTimeout(time.Minute))

		entries := auditEntries(ctx, fmt.Sprintf(`{job="audit/k3s"} |= %q | json | objectRef_resource="roles" | verb="create"`, name), 15*time.Minute)
		var event struct {
			Level       string            `json:"level"`
			Annotations map[string]string `json:"annotations"`
		}
		Expect(json.Unmarshal([]byte(entries[0].Line), &event)).To(Succeed())
		Expect(event.Level).To(Equal("Request"))
		Expect(event.Annotations["audit.k8s.io/truncated"]).To(Equal("true"), "the event kept its body")
	}, SpecTimeout(15*time.Minute))
})

// Loki's ruler loaded every alert rule, and each evaluates
var _ = Describe("SecurityLogsRulesLoaded", Label("sandbox", "testbed", "security-logs", "logs"), func() {
	It("evaluates every audit rule", func(ctx SpecContext) {
		Eventually(ctx, func() bool {
			out, err := kube.Kubectl(ctx, "loki", "get", "--raw",
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
			for name := range securitylogs.AuditAlerts {
				if !healthy[name] {
					GinkgoWriter.Printf("%s: not loaded or not ok yet\n", name)
					return false
				}
			}
			return true
		}).WithTimeout(10*time.Minute).WithPolling(30*time.Second).Should(BeTrue(), "Loki's ruler does not evaluate every audit rule")
	}, SpecTimeout(15*time.Minute))
})
