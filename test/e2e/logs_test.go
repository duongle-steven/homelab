package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type lokiEntry struct {
	Labels map[string]string
	Line   string
}

// lokiQuery runs a LogQL range query over the last `since` through the API server's service proxy
func lokiQuery(ctx context.Context, query string, since time.Duration) ([]lokiEntry, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", fmt.Sprint(time.Now().Add(-since).UnixNano()))
	params.Set("limit", "1000")
	out, err := kube.Kubectl(ctx, "loki", "get", "--raw",
		"/api/v1/namespaces/loki/services/loki:3100/proxy/loki/api/v1/query_range?"+params.Encode())
	if err != nil {
		return nil, err
	}
	var res struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, err
	}
	var entries []lokiEntry
	for _, r := range res.Data.Result {
		for _, v := range r.Values {
			entries = append(entries, lokiEntry{Labels: r.Stream, Line: v[1]})
		}
	}
	return entries, nil
}

// Grafana queries Loki through its data source (uid loki), in the sandbox too
var _ = Describe("LogsGrafanaDataSource", Label("sandbox", "testbed", "logs"), func() {
	It("is healthy", func(ctx SpecContext) {
		password := secretValue(ctx, "grafana", "grafana-admin", "admin-password")
		Eventually(ctx, func() bool {
			var health struct {
				Status string `json:"status"`
			}
			status, err := getJSONE(ctx, http.MethodGet, "https://"+grafanaHost+"/api/datasources/uid/loki/health", nil, "admin", password, nil, &health)
			return err == nil && status == http.StatusOK && health.Status == "OK"
		}).WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "Grafana's Loki data source is not healthy")
	}, SpecTimeout(25*time.Minute))
})

// Prometheus scrapes Loki and both Collectors, and the agent has sent log records to Loki
var _ = Describe("LogsTargetsUp", Label("sandbox", "testbed", "logs"), func() {
	It("scrapes Loki and the Collectors", func(ctx SpecContext) {
		Eventually(ctx, func() bool {
			metrics, err := prometheusQuery(ctx, `count by (namespace, job) (up{namespace=~"loki|opentelemetry"} == 1)`)
			if err != nil {
				return false
			}
			var loki, agent, gateway bool
			for _, m := range metrics {
				switch {
				case strings.Contains(m["job"], "agent-collector"):
					agent = true
				case strings.Contains(m["job"], "gateway-collector"):
					gateway = true
				case m["namespace"] == "loki" && strings.Contains(m["job"], "loki"):
					loki = true
				}
			}
			if !loki || !agent || !gateway {
				return false
			}
			sent, err := prometheusQuery(ctx, `sum(otelcol_exporter_sent_log_records{namespace="opentelemetry"}) > 0`)
			return err == nil && len(sent) > 0
		}).WithTimeout(20*time.Minute).WithPolling(30*time.Second).Should(BeTrue(), "Loki's or the Collectors' targets are not up, or no log record was sent")
	}, SpecTimeout(25*time.Minute))
})

const logsNamespace = "logs-test"

// logPod starts a pod in logsNamespace that sleeps; the spec makes it print with printTo
func logPod(ctx context.Context, name string) {
	GinkgoHelper()
	_, _ = kube.Kubectl(ctx, "", "create", "namespace", logsNamespace)
	_, _ = kube.Kubectl(ctx, logsNamespace, "delete", "pod", name, "--ignore-not-found", "--wait=true")
	out, err := kube.Kubectl(ctx, logsNamespace, "run", name, "--image=curlimages/curl:8.23.0", "--restart=Never",
		"--labels=app.kubernetes.io/name=logs-test", "--command", "--", "sleep", "3600")
	Expect(err).NotTo(HaveOccurred(), out)
	out, err = kube.Kubectl(ctx, logsNamespace, "wait", "--for=condition=Ready", "pod/"+name, "--timeout=5m")
	Expect(err).NotTo(HaveOccurred(), out)
	DeferCleanup(func(ctx SpecContext) {
		_, _ = kube.Kubectl(ctx, logsNamespace, "delete", "pod", name, "--wait=false")
	}, NodeTimeout(time.Minute))
}

// printTo writes a line to the pod's stdout (its container log)
func printTo(ctx context.Context, pod, line string) {
	GinkgoHelper()
	out, err := kube.Kubectl(ctx, logsNamespace, "exec", pod, "--", "sh", "-c", "echo "+line+" > /proc/1/fd/1")
	Expect(err).NotTo(HaveOccurred(), out)
}

func markerEntries(ctx context.Context, pod, marker string) []lokiEntry {
	entries, err := lokiQuery(ctx, fmt.Sprintf(`{k8s_namespace_name=%q, k8s_pod_name=%q} |= %q`, logsNamespace, pod, marker), time.Hour)
	if err != nil {
		return nil
	}
	return entries
}

// restartAgent replaces the OpenTelemetry agent's pods and waits for its DaemonSet
func restartAgent(ctx context.Context, wait bool) {
	GinkgoHelper()
	out, err := kube.Kubectl(ctx, "opentelemetry", "delete", "pod", "-l", "app.kubernetes.io/name=agent-collector", "--wait=true")
	Expect(err).NotTo(HaveOccurred(), out)
	if wait {
		waitForAgent(ctx)
	}
}

func waitForAgent(ctx context.Context) {
	GinkgoHelper()
	out, err := kube.Kubectl(ctx, "opentelemetry", "rollout", "status", "daemonset/agent-collector", "--timeout=5m")
	Expect(err).NotTo(HaveOccurred(), out)
}

// A pod's line reaches Loki once, with its labels
var _ = Describe("LogsPodLines", Label("sandbox", "testbed", "logs"), func() {
	It("delivers a line once with its labels", func(ctx SpecContext) {
		logPod(ctx, "lines")
		marker := fmt.Sprint("logs-marker-", time.Now().UnixNano())
		printTo(ctx, "lines", marker)
		var entries []lokiEntry
		Eventually(ctx, func() bool {
			entries = markerEntries(ctx, "lines", marker)
			return len(entries) > 0
		}).WithTimeout(10*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "the marker did not reach Loki")
		sleep(ctx, 30*time.Second) // a second tail of the same file would deliver it again by now
		entries = markerEntries(ctx, "lines", marker)
		Expect(entries).To(HaveLen(1), "the marker arrived more than once")
		labels := entries[0].Labels
		// OpenTelemetry's labels (the node is structured metadata, which Loki returns with the stream's labels)
		Expect(labels["k8s_namespace_name"]).To(Equal(logsNamespace))
		Expect(labels["k8s_pod_name"]).To(Equal("lines"))
		Expect(labels["k8s_container_name"]).To(Equal("lines"))
		Expect(labels["service_name"]).To(Equal("logs-test"))
		Expect(labels["k8s_node_name"]).NotTo(BeEmpty())
	}, SpecTimeout(20*time.Minute))
})

// A restarted agent resumes from its positions (file_storage on the node): no line lost, none repeated
var _ = Describe("LogsResumeAfterRestart", Label("sandbox", "testbed", "logs"), func() {
	It("resumes from its positions", func(ctx SpecContext) {
		logPod(ctx, "resume")
		first := fmt.Sprint("logs-first-", time.Now().UnixNano())
		printTo(ctx, "resume", first)
		Eventually(ctx, func() bool { return len(markerEntries(ctx, "resume", first)) > 0 }).
			WithTimeout(10*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "the first marker did not reach Loki")

		restartAgent(ctx, false)
		second := fmt.Sprint("logs-second-", time.Now().UnixNano())
		printTo(ctx, "resume", second) // written while the agent restarts
		waitForAgent(ctx)
		Eventually(ctx, func() bool { return len(markerEntries(ctx, "resume", second)) > 0 }).
			WithTimeout(10*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "the line written during the restart was lost")
		Expect(markerEntries(ctx, "resume", first)).To(HaveLen(1), "the restarted agent sent the first marker again")
	}, SpecTimeout(35*time.Minute))
})

// Kubernetes events land in Loki (kept 30 days instead of the API server's hour)
var _ = Describe("LogsEvents", Label("sandbox", "testbed", "logs"), func() {
	It("delivers a pod's events", func(ctx SpecContext) {
		logPod(ctx, "events")
		Eventually(ctx, func() bool {
			entries, err := lokiQuery(ctx, fmt.Sprintf(`{service_name="kubernetes-events"} |= %q |= "events"`, logsNamespace), time.Hour)
			if err != nil {
				return false
			}
			for _, e := range entries {
				if strings.Contains(e.Line, "Scheduled") || strings.Contains(e.Line, "Started") {
					return true
				}
			}
			return false
		}).WithTimeout(10*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "the pod's events did not reach Loki")
	}, SpecTimeout(20*time.Minute))
})

// Lines read while Loki is unreachable survive a restart of the agent (its queue in file_storage on the node) and
// arrive once Loki is back
var _ = Describe("LogsSurviveLokiOutage", Label("sandbox", "testbed", "logs"), func() {
	It("keeps lines read during an outage", func(ctx SpecContext) {
		logPod(ctx, "outage")
		// Argo CD does not prune what it does not manage: this policy lives only for the spec
		deny := `apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: logs-test-outage
  namespace: loki
spec:
  action: DENY
  selector:
    matchLabels:
      app.kubernetes.io/name: loki
  rules:
    - from:
        - source:
            principals: ["cluster.local/ns/opentelemetry/sa/agent-collector"]
`
		Expect(kube.Apply(ctx, "loki", deny)).To(Succeed())
		removed := false
		// Retried until it is gone: a deny left behind (the API server restarting mid-spec, 2026-11-15) cut the agent
		// off from Loki and failed every log spec after it
		remove := func(ctx context.Context) {
			if removed {
				return
			}
			Eventually(ctx, func() error {
				_, err := kube.Kubectl(ctx, "loki", "delete", "authorizationpolicy", "logs-test-outage", "--ignore-not-found")
				return err
			}).WithTimeout(10*time.Minute).WithPolling(10*time.Second).Should(Succeed(), "the outage's deny policy is still in loki")
			removed = true
		}
		DeferCleanup(func(ctx SpecContext) { remove(ctx) }, NodeTimeout(11*time.Minute))
		sleep(ctx, 15*time.Second) // ztunnel picks up the policy
		// ztunnel checks policies when a connection opens: restart the agent so its keep-alive connection to Loki is a
		// new, denied one
		restartAgent(ctx, true)

		marker := fmt.Sprint("logs-outage-", time.Now().UnixNano())
		printTo(ctx, "outage", marker)
		sleep(ctx, 30*time.Second) // the agent reads the line, moves its positions past it and queues it
		Expect(markerEntries(ctx, "outage", marker)).To(BeEmpty(), "the line reached Loki through the denied connection")
		restartAgent(ctx, true)
		remove(ctx)
		Eventually(ctx, func() bool { return len(markerEntries(ctx, "outage", marker)) > 0 }).
			WithTimeout(10*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "the line read during Loki's outage was lost")
		Expect(markerEntries(ctx, "outage", marker)).To(HaveLen(1), "the line arrived more than once")
	}, SpecTimeout(30*time.Minute))
})
