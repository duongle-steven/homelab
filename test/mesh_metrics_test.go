package test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Prometheus scrapes Istio (system/istio-system's PodMonitors): ztunnel's TCP metrics between workloads, the Gateways'
// HTTP metrics and istiod's pushes. Kiali's graph (system/kiali) and Grafana read them.
func TestMeshMetrics(t *testing.T) {
	meshSandboxOnly(t)
	app := smokeApps("dev")[0]
	host := routeHost(t, app.namespace, app.name)
	for name, query := range map[string]string{
		"ztunnel":  `istio_tcp_sent_bytes_total{reporter="destination"}`,
		"gateways": `istio_requests_total{source_workload="gateway-istio"}`,
		"istiod":   `pilot_xds_pushes`,
	} {
		t.Run(name, func(t *testing.T) {
			assert.Eventually(t, func() bool {
				appCodes(map[string]string{app.name: host})
				series, err := prometheusQuery(t, query)
				return err == nil && len(series) > 0
			}, 5*time.Minute, 15*time.Second, "no series for %s", query)
		})
	}
}
