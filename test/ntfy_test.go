package test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ntfyURL = "https://ntfy.127-0-0-1.nip.io"

// ntfyRequest sends method to path with a bearer token (none when empty) and returns the status and body
func ntfyRequest(t *testing.T, method, path, token, body string) (int, string) {
	req, err := http.NewRequest(method, ntfyURL+path, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := insecureClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func waitForNtfy(t *testing.T) {
	require.Eventually(t, func() bool {
		status, _ := ntfyRequest(t, http.MethodGet, "/v1/health", "", "")
		return status == http.StatusOK
	}, 20*time.Minute, 15*time.Second, "ntfy does not answer through the Gateway")
}

// ntfyToken is a user's ntfy token: the generated value with ntfy's tk_ prefix
func ntfyToken(t *testing.T, user string) string {
	return "tk_" + secretValue(t, "global-secrets", "ntfy."+user+"-token", "token")
}

// Published through the Tunnel in production: nothing without credentials
func TestNtfyDeniesAnonymous(t *testing.T) {
	secretsSandboxOnly(t)
	waitForNtfy(t)
	status, _ := ntfyRequest(t, http.MethodPost, "/alerts", "", "anonymous")
	assert.Equal(t, http.StatusForbidden, status, "anonymous publish")
	status, _ = ntfyRequest(t, http.MethodGet, "/alerts/json?poll=1", "", "")
	assert.Equal(t, http.StatusForbidden, status, "anonymous subscribe")
	status, _ = ntfyRequest(t, http.MethodPost, "/v1/account", "", `{"username":"x","password":"y"}`)
	assert.NotEqual(t, http.StatusOK, status, "signup is off")
}

// phone reads topic alerts and cannot publish; alerts publishes and cannot read
func TestNtfyUsers(t *testing.T) {
	secretsSandboxOnly(t)
	waitForNtfy(t)
	phone, alerts := ntfyToken(t, "phone"), ntfyToken(t, "alerts")
	status, _ := ntfyRequest(t, http.MethodPost, "/alerts", phone, "from phone")
	assert.Equal(t, http.StatusForbidden, status, "phone published")
	status, _ = ntfyRequest(t, http.MethodGet, "/alerts/json?poll=1", alerts, "")
	assert.Equal(t, http.StatusForbidden, status, "alerts subscribed")

	marker := fmt.Sprint("ntfy-test-", time.Now().UnixNano())
	status, body := ntfyRequest(t, http.MethodPost, "/alerts", alerts, marker)
	require.Equal(t, http.StatusOK, status, body)
	status, body = ntfyRequest(t, http.MethodGet, "/alerts/json?poll=1&since=10m", phone, "")
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, marker)
}

// postAlert posts one alert to Alertmanager's API from inside its pod. Its namespace label is its name: a group of its
// own (group_by namespace), so it is sent after group_wait, not after an earlier test alert's group_interval
func postAlert(t *testing.T, name, severity string) {
	alert := fmt.Sprintf(`[{"labels":{"alertname":%q,"namespace":%q,"severity":%q}}]`, name, strings.ToLower(name), severity)
	k8s.RunKubectl(t, k8s.NewKubectlOptions("", "", "monitoring-system"), "exec",
		"alertmanager-monitoring-system-kube-pro-alertmanager-0", "-c", "alertmanager", "--",
		"wget", "-qO-", "--header=Content-Type: application/json", "--post-data="+alert,
		"http://localhost:9093/api/v2/alerts")
}

// ntfyMessage waits for a message in topic alerts (read as phone) whose text contains marker
func ntfyMessage(t *testing.T, marker string) map[string]any {
	phone := ntfyToken(t, "phone")
	var found map[string]any
	require.Eventually(t, func() bool {
		status, body := ntfyRequest(t, http.MethodGet, "/alerts/json?poll=1&since=10m", phone, "")
		if status != http.StatusOK {
			return false
		}
		for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil && strings.Contains(line, marker) {
				found = m
				return true
			}
		}
		return false
	}, 3*time.Minute, 10*time.Second, "no message with %s in topic alerts", marker)
	return found
}

// An alert posted to Alertmanager reaches topic alerts through ntfy's alertmanager template
func TestNtfyAlertmanagerDelivers(t *testing.T) {
	secretsSandboxOnly(t)
	waitForNtfy(t)
	name := fmt.Sprint("NtfyTest", time.Now().UnixNano())
	postAlert(t, name, "warning")
	ntfyMessage(t, name)
}

// Critical alerts arrive with ntfy's highest priority, so they stand out on phones
func TestNtfyAlertmanagerCriticalPriority(t *testing.T) {
	secretsSandboxOnly(t)
	waitForNtfy(t)
	name := fmt.Sprint("NtfyCritical", time.Now().UnixNano())
	postAlert(t, name, "critical")
	assert.EqualValues(t, 5, ntfyMessage(t, name)["priority"], "critical alert priority")
}

// The Watchdog keeps a delayed message scheduled (sequence watchdog): it fires only if heartbeats stop
func TestNtfyWatchdogScheduled(t *testing.T) {
	secretsSandboxOnly(t)
	waitForNtfy(t)
	phone := ntfyToken(t, "phone")
	assert.Eventually(t, func() bool {
		status, body := ntfyRequest(t, http.MethodGet, "/alerts/json?poll=1&sched=1", phone, "")
		return status == http.StatusOK && strings.Contains(body, "Monitoring heartbeat missing")
	}, 10*time.Minute, 15*time.Second, "no scheduled watchdog message")
}
