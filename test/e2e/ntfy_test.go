package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var ntfyURL = "https://ntfy." + suiteDomain

// ntfyRequest sends method to path with a bearer token (none when empty) and returns the status and body
func ntfyRequest(ctx context.Context, method, path, token, body string) (int, string) {
	req, err := http.NewRequestWithContext(ctx, method, ntfyURL+path, strings.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
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

// ntfyUp waits until ntfy answers through the Gateway
func ntfyUp(ctx context.Context) {
	Eventually(ctx, func() bool {
		status, _ := ntfyRequest(ctx, http.MethodGet, "/v1/health", "", "")
		return status == http.StatusOK
	}).WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "ntfy does not answer through the Gateway")
}

// waitForNtfy is a container's BeforeEach running ntfyUp
func waitForNtfy() {
	BeforeEach(func(ctx SpecContext) { ntfyUp(ctx) }, NodeTimeout(25*time.Minute))
}

// ntfyToken is a user's ntfy token: the generated value in OpenBao with ntfy's tk_ prefix
func ntfyToken(ctx context.Context, user string) string {
	return "tk_" + generatedValue(ctx, "ntfy/generated/"+user+"-token", "token")
}

// Published through the Tunnel in production: nothing without credentials
var _ = Describe("NtfyDeniesAnonymous", Label("sandbox", "testbed", "ntfy"), func() {
	waitForNtfy()

	It("gives nothing without credentials", func(ctx SpecContext) {
		status, _ := ntfyRequest(ctx, http.MethodPost, "/alerts", "", "anonymous")
		Expect(status).To(Equal(http.StatusForbidden), "anonymous publish")
		status, _ = ntfyRequest(ctx, http.MethodGet, "/alerts/json?poll=1", "", "")
		Expect(status).To(Equal(http.StatusForbidden), "anonymous subscribe")
		status, _ = ntfyRequest(ctx, http.MethodPost, "/v1/account", "", `{"username":"x","password":"y"}`)
		Expect(status).NotTo(Equal(http.StatusOK), "signup is off")
	}, SpecTimeout(5*time.Minute))
})

// phone reads topic alerts and cannot publish; alerts publishes and cannot read
var _ = Describe("NtfyUsers", Label("sandbox", "testbed", "ntfy"), func() {
	waitForNtfy()

	It("lets phone read and alerts publish", func(ctx SpecContext) {
		phone, alerts := ntfyToken(ctx, "phone"), ntfyToken(ctx, "alerts")
		status, _ := ntfyRequest(ctx, http.MethodPost, "/alerts", phone, "from phone")
		Expect(status).To(Equal(http.StatusForbidden), "phone published")
		status, _ = ntfyRequest(ctx, http.MethodGet, "/alerts/json?poll=1", alerts, "")
		Expect(status).To(Equal(http.StatusForbidden), "alerts subscribed")

		marker := fmt.Sprint("ntfy-test-", time.Now().UnixNano())
		status, body := ntfyRequest(ctx, http.MethodPost, "/alerts", alerts, marker)
		Expect(status).To(Equal(http.StatusOK), body)
		status, body = ntfyRequest(ctx, http.MethodGet, "/alerts/json?poll=1&since=10m", phone, "")
		Expect(status).To(Equal(http.StatusOK), body)
		Expect(body).To(ContainSubstring(marker))
	}, SpecTimeout(45*time.Minute))
})

// postAlert posts one alert to Alertmanager's API from inside its pod. Its namespace label is its name: a group of its
// own (group_by namespace), so it is sent after group_wait, not after an earlier test alert's group_interval
func postAlert(ctx context.Context, name, severity string) {
	alert := fmt.Sprintf(`[{"labels":{"alertname":%q,"namespace":%q,"severity":%q}}]`, name, strings.ToLower(name), severity)
	out, err := kube.Kubectl(ctx, "monitoring-system", "exec",
		"alertmanager-monitoring-system-kube-pro-alertmanager-0", "-c", "alertmanager", "--",
		"wget", "-qO-", "--header=Content-Type: application/json", "--post-data="+alert,
		"http://localhost:9093/api/v2/alerts")
	Expect(err).NotTo(HaveOccurred(), out)
}

// ntfyMessage waits for a message in topic alerts (read as phone) whose text contains marker
func ntfyMessage(ctx context.Context, marker string) map[string]any {
	phone := ntfyToken(ctx, "phone")
	var found map[string]any
	Eventually(ctx, func() bool {
		status, body := ntfyRequest(ctx, http.MethodGet, "/alerts/json?poll=1&since=10m", phone, "")
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
	}).WithTimeout(3*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "no message with %s in topic alerts", marker)
	return found
}

// An alert posted to Alertmanager reaches topic alerts through ntfy's alertmanager template
var _ = Describe("NtfyAlertmanagerDelivers", Label("sandbox", "testbed", "ntfy"), func() {
	waitForNtfy()

	It("delivers an alert", func(ctx SpecContext) {
		name := fmt.Sprint("NtfyTest", time.Now().UnixNano())
		postAlert(ctx, name, "warning")
		ntfyMessage(ctx, name)
	}, SpecTimeout(30*time.Minute))
})

// Critical alerts arrive with ntfy's highest priority, so they stand out on phones
var _ = Describe("NtfyAlertmanagerCriticalPriority", Label("sandbox", "testbed", "ntfy"), func() {
	waitForNtfy()

	It("sends critical alerts at priority 5", func(ctx SpecContext) {
		name := fmt.Sprint("NtfyCritical", time.Now().UnixNano())
		postAlert(ctx, name, "critical")
		Expect(ntfyMessage(ctx, name)["priority"]).To(BeEquivalentTo(5), "critical alert priority")
	}, SpecTimeout(30*time.Minute))
})

// The Watchdog keeps a delayed message scheduled (sequence watchdog): it fires only if heartbeats stop
var _ = Describe("NtfyWatchdogScheduled", Label("sandbox", "testbed", "ntfy"), func() {
	waitForNtfy()

	It("keeps the watchdog message scheduled", func(ctx SpecContext) {
		phone := ntfyToken(ctx, "phone")
		Eventually(ctx, func() bool {
			status, body := ntfyRequest(ctx, http.MethodGet, "/alerts/json?poll=1&sched=1", phone, "")
			return status == http.StatusOK && strings.Contains(body, "Monitoring heartbeat missing")
		}).WithTimeout(10*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "no scheduled watchdog message")
	}, SpecTimeout(35*time.Minute))
})
