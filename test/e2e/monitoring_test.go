package e2e

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// prometheusQuery runs an instant query through the API server's service proxy and returns each result's labels.
func prometheusQuery(ctx context.Context, query string) ([]map[string]string, error) {
	out, err := kube.Kubectl(ctx, "monitoring-system", "get", "--raw",
		"/api/v1/namespaces/monitoring-system/services/monitoring-system-kube-pro-prometheus:9090/proxy/api/v1/query?query="+url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	var res struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, err
	}
	metrics := make([]map[string]string, 0, len(res.Data.Result))
	for _, r := range res.Data.Result {
		metrics = append(metrics, r.Metric)
	}
	return metrics, nil
}

// Prometheus finds the control-plane nodes itself (ScrapeConfig, node discovery): no address list to keep in step
var _ = Describe("MonitoringControlPlaneTargets", Label("sandbox", "testbed", "monitoring"), func() {
	It("scrapes every control-plane job", func(ctx SpecContext) {
		jobs := []string{"kube-controller-manager", "kube-scheduler", "kube-etcd", "kube-proxy"}
		Eventually(ctx, func() bool {
			metrics, err := prometheusQuery(ctx, `count by (job) (up{job=~"kube-controller-manager|kube-scheduler|kube-etcd|kube-proxy"} == 1)`)
			if err != nil {
				return false
			}
			found := map[string]bool{}
			for _, m := range metrics {
				found[m["job"]] = true
			}
			for _, job := range jobs {
				if !found[job] {
					GinkgoWriter.Printf("no target up for job %s\n", job)
					return false
				}
			}
			return true
		}).WithTimeout(5*time.Minute).WithPolling(30*time.Second).Should(BeTrue(), "control-plane jobs have no target up")
	}, SpecTimeout(10*time.Minute))
})

// ClusterShutdownMarkerStuck reads the marker's metric: the rule loaded, and kube-state-metrics exports the ConfigMap
var _ = Describe("MonitoringClusterShutdownMarker", Label("sandbox", "testbed", "monitoring"), func() {
	It("exports the shutdown marker", func(ctx SpecContext) {
		Eventually(ctx, func() bool {
			rules, err := kube.Kubectl(ctx, "monitoring-system", "get", "--raw",
				"/api/v1/namespaces/monitoring-system/services/monitoring-system-kube-pro-prometheus:9090/proxy/api/v1/rules?type=alert")
			return err == nil && strings.Contains(rules, "ClusterShutdownMarkerStuck")
		}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "the alert is not loaded")

		_, err := kube.Kubectl(ctx, "kube-system", "create", "configmap", "cluster-shutdown", "--from-literal=replicas=1")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) {
			_, err := kube.Kubectl(ctx, "kube-system", "delete", "configmap", "cluster-shutdown", "--ignore-not-found")
			Expect(err).NotTo(HaveOccurred())
		}, NodeTimeout(time.Minute))
		Eventually(ctx, func() bool {
			metrics, err := prometheusQuery(ctx, `kube_configmap_created{namespace="kube-system",configmap="cluster-shutdown"}`)
			return err == nil && len(metrics) > 0
		}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "the marker's metric never appeared")
	}, SpecTimeout(15*time.Minute))
})
