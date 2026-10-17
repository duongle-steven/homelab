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
