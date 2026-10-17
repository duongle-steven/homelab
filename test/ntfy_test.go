package test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

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
