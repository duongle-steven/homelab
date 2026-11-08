package e2e

import (
	"net/http"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/smoke"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var kialiHost = "kiali." + suiteDomain

// An admin signs in through Kanidm and sees the mesh; Kiali refuses to change anything; nobody else gets in
var _ = Describe("SSOKiali", Label("sandbox", "testbed", "sso", "kiali"), func() {
	base := "https://" + kialiHost + "/kiali"
	start := base + "/api/auth/openid_redirect"
	waitForIdentity()
	waitForAppBeforeEach(base + "/healthz")

	It("dev-admin", func(ctx SpecContext) {
		cookies := appLogin(ctx, "dev-admin", ensureCredentials(ctx, "dev-admin"), start, kialiHost)
		var namespaces []struct {
			Name string `json:"name"`
		}
		Expect(getJSON(ctx, "GET", base+"/api/namespaces", cookies, "", "", nil, &namespaces)).To(Equal(http.StatusOK))
		var names []string
		for _, n := range namespaces {
			names = append(names, n.Name)
		}
		Expect(names).To(ContainElement("kiali"))

		// The graph has edges: ztunnel's metrics (system/istio-system's PodMonitors) reach Kiali through Prometheus
		app := smoke.Apps("dev")[0]
		host := routeHost(ctx, app.Namespace, app.Name)
		Eventually(ctx, func() bool {
			appCodes(map[string]string{app.Name: host})
			var graph struct {
				Elements struct {
					Edges []any `json:"edges"`
				} `json:"elements"`
			}
			status, err := getJSONE(ctx, "GET", base+"/api/namespaces/graph?graphType=workload&duration=600s&namespaces="+app.Namespace+",istio-system",
				cookies, "", "", nil, &graph)
			return err == nil && status == http.StatusOK && len(graph.Elements.Edges) > 0
		}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "Kiali's graph has no edge")

		// A change to an Istio object is refused: Kiali's role only reads (the API server refuses it), and view_only_mode
		// hides the UI's write actions
		status, _ := getJSONE(ctx, "PATCH", base+"/api/namespaces/istio-system/istio/security.istio.io/v1/AuthorizationPolicy/deny-all",
			cookies, "", "", map[string]any{"metadata": map[string]any{"labels": map[string]string{"kiali-test": "x"}}}, nil)
		Expect(status).NotTo(Equal(http.StatusOK), "Kiali changed an AuthorizationPolicy")
		label, err := kube.Kubectl(ctx, "istio-system", "get", "authorizationpolicies.security.istio.io", "deny-all",
			"-o", "jsonpath={.metadata.labels.kiali-test}")
		Expect(err).NotTo(HaveOccurred(), label)
		Expect(strings.TrimSpace(label)).To(BeEmpty(), "the AuthorizationPolicy got Kiali's label")
	}, SpecTimeout(45*time.Minute))

	for _, account := range []string{"dev-editor", "dev-viewer", "dev-outsider"} {
		It(account, func(ctx SpecContext) {
			Expect(browserDenied(ctx, account, ensureCredentials(ctx, account), start)).To(ContainSubstring("Access Denied"))
		}, SpecTimeout(15*time.Minute))
	}
})
