package e2e

import (
	"time"

	"github.com/duongle-steven/homelab/test/internal/smoke"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Prometheus scrapes Istio (system/istio-system's PodMonitors): ztunnel's TCP metrics between workloads, the Gateways'
// HTTP metrics and istiod's pushes. Kiali's graph (system/kiali) and Grafana read them.
var _ = Describe("MeshMetrics", Label("sandbox", "testbed", "mesh"), func() {
	for _, c := range []struct{ name, query string }{
		{"gateways", `istio_requests_total{source_workload="gateway-istio"}`},
		{"istiod", `pilot_xds_pushes`},
		{"ztunnel", `istio_tcp_sent_bytes_total{reporter="destination"}`},
	} {
		It(c.name, func(ctx SpecContext) {
			app := smoke.Apps("dev")[0]
			host := routeHost(ctx, app.Namespace, app.Name)
			Eventually(ctx, func() bool {
				appCodes(ctx, map[string]string{app.Name: host})
				series, err := prometheusQuery(ctx, c.query)
				return err == nil && len(series) > 0
			}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "no series for %s", c.query)
		}, SpecTimeout(40*time.Minute))
	}
})
