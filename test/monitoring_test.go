package test

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
)

// prometheusQuery runs an instant query through the API server's service proxy and returns each result's labels.
func prometheusQuery(t *testing.T, query string) ([]map[string]string, error) {
	options := k8s.NewKubectlOptions("", "", "monitoring-system")
	options.Logger = logger.Discard
	out, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "--raw",
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
func TestMonitoringControlPlaneTargets(t *testing.T) {
	meshSandboxOnly(t)
	jobs := []string{"kube-controller-manager", "kube-scheduler", "kube-etcd", "kube-proxy"}
	assert.Eventually(t, func() bool {
		metrics, err := prometheusQuery(t, `count by (job) (up{job=~"kube-controller-manager|kube-scheduler|kube-etcd|kube-proxy"} == 1)`)
		if err != nil {
			return false
		}
		found := map[string]bool{}
		for _, m := range metrics {
			found[m["job"]] = true
		}
		for _, job := range jobs {
			if !found[job] {
				t.Logf("no target up for job %s", job)
				return false
			}
		}
		return true
	}, 5*time.Minute, 30*time.Second, "control-plane jobs have no target up")
}
