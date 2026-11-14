package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// randomHex is n random bytes in hex: a W3C trace or span id
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Step OpenTelemetry: a request through the LAN Gateway leaves its span in Tempo (through the gateway Collector) and an
// access log line in Loki carrying the same trace id, the link Grafana follows from a log line to its trace
var _ = Describe("TracesFromGateway", Label("sandbox", "traces"), func() {
	It("traces a request and logs its trace id", func(ctx SpecContext) {
		password := secretValue(ctx, "grafana", "grafana-admin", "admin-password")
		traceID := randomHex(16)
		traceparent := fmt.Sprintf("00-%s-%s-01", traceID, randomHex(8))
		Eventually(ctx, func() bool {
			status, err := getJSONE(ctx, http.MethodGet, "https://"+grafanaHost+"/api/health", nil, "", "", nil, nil, "traceparent", traceparent)
			if err != nil || status != http.StatusOK {
				return false
			}
			// Tempo through Grafana's data source (Grafana is who may read Tempo)
			var trace map[string]any
			status, err = getJSONE(ctx, http.MethodGet, "https://"+grafanaHost+"/api/datasources/proxy/uid/tempo/api/v2/traces/"+traceID,
				nil, "admin", password, nil, &trace)
			return err == nil && status == http.StatusOK && strings.Contains(fmt.Sprint(trace), "gateway")
		}).WithTimeout(10*time.Minute).WithPolling(20*time.Second).Should(BeTrue(), "no trace %s from the Gateway in Tempo", traceID)

		Eventually(ctx, func() bool {
			entries, err := lokiQuery(ctx, fmt.Sprintf(`{k8s_namespace_name="istio-system"} |= %q`, traceID), 30*time.Minute)
			if err != nil {
				return false
			}
			for _, e := range entries {
				if strings.Contains(e.Line, `"trace_id":"`+traceID+`"`) {
					return true
				}
			}
			return false
		}).WithTimeout(10*time.Minute).WithPolling(20*time.Second).Should(BeTrue(), "no access log line with trace id %s in Loki", traceID)
	}, SpecTimeout(25*time.Minute))
})

// The gateway Collector takes OTLP only from its named senders: a pod of another meshed namespace is refused
var _ = Describe("OTLPRefusedFromOthers", Label("sandbox", "traces"), func() {
	It("refuses OTLP from a namespace that has no reason to send", func(ctx SpecContext) {
		logPod(ctx, "otlp")
		out, _ := kube.Kubectl(ctx, logsNamespace, "exec", "otlp", "--", "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
			"--max-time", "10", "-X", "POST", "-H", "Content-Type: application/json", "-d", `{"resourceSpans":[]}`,
			"http://gateway-collector.opentelemetry:4318/v1/traces")
		Expect(strings.TrimSpace(out)).NotTo(HavePrefix("2"), "the gateway Collector accepted OTLP from %s", logsNamespace)
	}, SpecTimeout(10*time.Minute))
})
