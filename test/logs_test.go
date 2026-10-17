package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func logsSandboxOnly(t *testing.T) { secretsSandboxOnly(t) }

type lokiEntry struct {
	Labels map[string]string
	Line   string
}

// lokiQuery runs a LogQL range query over the last `since` through the API server's service proxy
func lokiQuery(t *testing.T, query string, since time.Duration) ([]lokiEntry, error) {
	options := k8s.NewKubectlOptions("", "", "loki")
	options.Logger = logger.Discard
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", fmt.Sprint(time.Now().Add(-since).UnixNano()))
	params.Set("limit", "1000")
	out, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "--raw",
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
func TestLogsGrafanaDataSource(t *testing.T) {
	logsSandboxOnly(t)
	password := secretValue(t, "grafana", "grafana-admin", "admin-password")
	require.Eventually(t, func() bool {
		var health struct {
			Status string `json:"status"`
		}
		status, err := getJSONE(http.MethodGet, "https://"+grafanaHost+"/api/datasources/uid/loki/health", nil, "admin", password, nil, &health)
		return err == nil && status == http.StatusOK && health.Status == "OK"
	}, 20*time.Minute, 15*time.Second, "Grafana's Loki data source is not healthy")
}

// Prometheus scrapes Loki and Alloy
func TestLogsTargetsUp(t *testing.T) {
	logsSandboxOnly(t)
	assert.Eventually(t, func() bool {
		metrics, err := prometheusQuery(t, `count by (job) (up{namespace="loki"} == 1)`)
		if err != nil {
			return false
		}
		var loki, alloy bool
		for _, m := range metrics {
			switch {
			case strings.Contains(m["job"], "alloy"):
				alloy = true
			case strings.Contains(m["job"], "loki"):
				loki = true
			}
		}
		return loki && alloy
	}, 20*time.Minute, 30*time.Second, "Loki's and Alloy's targets are not up")
}

const logsNamespace = "logs-test"

// logPod starts a pod in logsNamespace that sleeps; the test makes it print with printTo
func logPod(t *testing.T, name string) *k8s.KubectlOptions {
	options := k8s.NewKubectlOptions("", "", logsNamespace)
	_ = k8s.RunKubectlE(t, k8s.NewKubectlOptions("", "", ""), "create", "namespace", logsNamespace)
	_ = k8s.RunKubectlE(t, options, "delete", "pod", name, "--ignore-not-found", "--wait=true")
	k8s.RunKubectl(t, options, "run", name, "--image=curlimages/curl:8.23.0", "--restart=Never",
		"--labels=app.kubernetes.io/name=logs-test", "--command", "--", "sleep", "3600")
	k8s.RunKubectl(t, options, "wait", "--for=condition=Ready", "pod/"+name, "--timeout=5m")
	t.Cleanup(func() { _ = k8s.RunKubectlE(t, options, "delete", "pod", name, "--wait=false") })
	return options
}

// printTo writes a line to the pod's stdout (its container log)
func printTo(t *testing.T, options *k8s.KubectlOptions, pod, line string) {
	k8s.RunKubectl(t, options, "exec", pod, "--", "sh", "-c", "echo "+line+" > /proc/1/fd/1")
}

func markerEntries(t *testing.T, pod, marker string) []lokiEntry {
	entries, err := lokiQuery(t, fmt.Sprintf(`{namespace=%q, pod=%q} |= %q`, logsNamespace, pod, marker), time.Hour)
	if err != nil {
		return nil
	}
	return entries
}

// A pod's line reaches Loki once, with its labels
func TestLogsPodLines(t *testing.T) {
	logsSandboxOnly(t)
	options := logPod(t, "lines")
	marker := fmt.Sprint("logs-marker-", time.Now().UnixNano())
	printTo(t, options, "lines", marker)
	var entries []lokiEntry
	require.Eventually(t, func() bool {
		entries = markerEntries(t, "lines", marker)
		return len(entries) > 0
	}, 10*time.Minute, 10*time.Second, "the marker did not reach Loki")
	time.Sleep(30 * time.Second) // a second tail of the same file would deliver it again by now
	entries = markerEntries(t, "lines", marker)
	require.Len(t, entries, 1, "the marker arrived more than once")
	labels := entries[0].Labels
	assert.Equal(t, logsNamespace, labels["namespace"])
	assert.Equal(t, "lines", labels["pod"])
	assert.Equal(t, "lines", labels["container"])
	assert.Equal(t, "logs-test", labels["app"])
	assert.NotEmpty(t, labels["node"])
}

// A restarted Alloy resumes from its positions: no line lost, none repeated
func TestLogsResumeAfterRestart(t *testing.T) {
	logsSandboxOnly(t)
	options := logPod(t, "resume")
	first := fmt.Sprint("logs-first-", time.Now().UnixNano())
	printTo(t, options, "resume", first)
	require.Eventually(t, func() bool { return len(markerEntries(t, "resume", first)) > 0 },
		10*time.Minute, 10*time.Second, "the first marker did not reach Loki")

	loki := k8s.NewKubectlOptions("", "", "loki")
	k8s.RunKubectl(t, loki, "delete", "pod", "-l", "app.kubernetes.io/name=alloy", "--wait=true")
	second := fmt.Sprint("logs-second-", time.Now().UnixNano())
	printTo(t, options, "resume", second) // written while Alloy restarts
	k8s.RunKubectl(t, loki, "rollout", "status", "daemonset/loki-alloy", "--timeout=5m")
	require.Eventually(t, func() bool { return len(markerEntries(t, "resume", second)) > 0 },
		10*time.Minute, 10*time.Second, "the line written during the restart was lost")
	assert.Len(t, markerEntries(t, "resume", first), 1, "the restarted Alloy sent the first marker again")
}

// Kubernetes events land in Loki (kept 30 days instead of the API server's hour)
func TestLogsEvents(t *testing.T) {
	logsSandboxOnly(t)
	logPod(t, "events")
	require.Eventually(t, func() bool {
		entries, err := lokiQuery(t, fmt.Sprintf(`{job="kubernetes-events", namespace=%q} |= "events"`, logsNamespace), time.Hour)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if strings.Contains(e.Line, "Scheduled") || strings.Contains(e.Line, "Started") {
				return true
			}
		}
		return false
	}, 10*time.Minute, 15*time.Second, "the pod's events did not reach Loki")
}
