package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/routes"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// gateway is the part of a Gateway API Gateway the tests read.
type gateway struct {
	Status struct {
		Addresses []struct {
			Value string `json:"value"`
		} `json:"addresses"`
		Conditions []routes.Condition `json:"conditions"`
	} `json:"status"`
}

var _ = Describe("GatewayProgrammed", Label("sandbox", "testbed", "gateway"), func() {
	// Both get an address: gateway from ServiceLB (the k3d node), public its ClusterIP
	for _, name := range []string{"gateway", "public"} {
		It(name, func(ctx SpecContext) {
			var gw gateway
			Eventually(ctx, func() bool {
				gw = gateway{}
				return getResource(ctx, "istio-system", &gw, "gateway", name) && routes.ConditionsMet(gw.Status.Conditions, "Accepted", "Programmed")
			}).WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "Gateway %s is not programmed", name)
			Expect(gw.Status.Addresses).NotTo(BeEmpty())
			if name == "gateway" {
				// ServiceLB publishes it on the node the tests dial
				Expect(gw.Status.Addresses[0].Value).To(Equal(gatewayAddress()))
			}
		}, SpecTimeout(25*time.Minute))
	}
})

var _ = Describe("GatewayHTTPSRedirect", Label("sandbox", "testbed", "gateway"), func() {
	It("redirects HTTP to HTTPS", func(ctx SpecContext) {
		host := routeHost(ctx, "excalidraw", "excalidraw")
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+"/some/path?x=1", nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := insecureClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusMovedPermanently))
		location, err := resp.Location()
		Expect(err).NotTo(HaveOccurred())
		Expect(location.Scheme).To(Equal("https"))
		Expect(location.Hostname()).To(Equal(host))
		// Some Gateways write the default port (:443) into Location; browsers treat both forms the same
		Expect(location.Port()).To(BeElementOf("", "443"))
		Expect(location.RequestURI()).To(Equal("/some/path?x=1"))
	}, SpecTimeout(35*time.Minute))
})

var _ = Describe("GatewayNoIngress", Label("sandbox", "testbed", "gateway"), func() {
	It("leaves no Ingress", func(ctx SpecContext) {
		// Operators remove the Ingresses they created earlier on their own schedule (kaniop waits for Kanidm)
		var ingresses struct {
			Items []struct {
				Metadata struct {
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
				} `json:"metadata"`
			} `json:"items"`
		}
		Eventually(ctx, func() bool {
			ingresses.Items = nil
			return getResource(ctx, "default", &ingresses, "ingress", "--all-namespaces") && len(ingresses.Items) == 0
		}).WithTimeout(10*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), func() string {
			return fmt.Sprintf("every component uses Gateway API routes, left: %+v", ingresses.Items)
		})
		var namespace struct{}
		Expect(getResource(ctx, "default", &namespace, "namespace", "ingress-nginx")).To(BeFalse(), "ingress-nginx must be gone")
	}, SpecTimeout(15*time.Minute))
})

var _ = Describe("GatewayForgejoSSH", Label("sandbox", "testbed", "gateway", "forgejo"), func() {
	It("serves Forgejo's SSH banner", func(ctx SpecContext) {
		Eventually(ctx, func() bool {
			conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(gatewayAddress(), "22"))
			if err != nil {
				return false
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			banner, err := bufio.NewReader(conn).ReadString('\n')
			return err == nil && strings.HasPrefix(banner, "SSH-2.0-")
		}).WithTimeout(20*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "no SSH banner from forgejo on %s:22", gatewayAddress())
	}, SpecTimeout(25*time.Minute))
})

// GatewayZotLargePush uploads one 200 MiB blob through the Gateway, as an image push would.
var _ = Describe("GatewayZotLargePush", Label("sandbox", "testbed", "gateway", "zot"), func() {
	It("pushes a 200 MiB blob", func(ctx SpecContext) {
		base := "https://" + registryHost
		waitForApp(ctx, base+"/livez")
		password := generatedValue(ctx, "zot/generated/ci", "password")

		// Same content every run, so zot stores a single copy
		blob := make([]byte, 200<<20)
		rand.New(rand.NewSource(1)).Read(blob)
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(blob))

		client := *insecureClient
		client.Timeout = 5 * time.Minute

		req, err := http.NewRequestWithContext(ctx, "POST", base+"/v2/gateway-test/blobs/uploads/", nil)
		Expect(err).NotTo(HaveOccurred())
		req.SetBasicAuth("ci", password)
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusAccepted))
		location, err := resp.Location()
		Expect(err).NotTo(HaveOccurred())
		query := location.Query()
		query.Set("digest", digest)
		location.RawQuery = query.Encode()

		req, err = http.NewRequestWithContext(ctx, "PUT", location.String(), bytes.NewReader(blob))
		Expect(err).NotTo(HaveOccurred())
		req.SetBasicAuth("ci", password)
		req.Header.Set("Content-Type", "application/octet-stream")
		start := time.Now()
		resp, err = client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusCreated), "%s", body)
		GinkgoWriter.Printf("pushed 200 MiB through the Gateway in %s\n", time.Since(start).Round(time.Millisecond))
	}, SpecTimeout(55*time.Minute))
})

// Gateway public admits routes only from the public map's namespaces (system/istio-system/values.yaml public): an
// admin app's route there must stay refused, a listed one accepted
var _ = Describe("GatewayPublicAllowList", Label("sandbox", "testbed", "gateway", "public"), func() {
	It("admits only the public map's namespaces", func(ctx SpecContext) {
		host := "public-allow-probe." + suiteDomain
		route := func(namespace string) string {
			return `apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: public-allow-probe
  namespace: ` + namespace + `
spec:
  parentRefs:
    - {name: public, namespace: istio-system, sectionName: https}
  hostnames: [` + host + `]
  rules:
    - backendRefs:
        - {name: public-allow-probe, port: 80}
`
		}
		apply := func(manifest string) {
			Expect(kube.Apply(ctx, "", manifest)).To(Succeed())
		}
		// The parent's Accepted condition and its reason
		accepted := func(namespace string) (status, reason string) {
			var r struct {
				Status struct {
					Parents []struct {
						Conditions []struct{ Type, Status, Reason string } `json:"conditions"`
					} `json:"parents"`
				} `json:"status"`
			}
			if !getResource(ctx, namespace, &r, "httproute", "public-allow-probe") {
				return "", ""
			}
			for _, p := range r.Status.Parents {
				for _, c := range p.Conditions {
					if c.Type == "Accepted" {
						return c.Status, c.Reason
					}
				}
			}
			return "", ""
		}

		refused := "public-allow-test"
		apply("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + refused + "\n")
		DeferCleanup(func(ctx SpecContext) {
			_, _ = kube.Kubectl(ctx, "", "delete", "namespace", refused, "--wait=false")
		}, NodeTimeout(time.Minute))
		apply(route(refused))
		var status, reason string
		Eventually(ctx, func() bool { status, reason = accepted(refused); return status != "" }).
			WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "Gateway public never reported on a route from %s", refused)
		Expect(status).To(Equal("False"), "a namespace off the public map reaches Gateway public")
		Expect(reason).To(Equal("NotAllowedByListeners"))

		DeferCleanup(func(ctx SpecContext) {
			_, _ = kube.Kubectl(ctx, "ntfy", "delete", "httproute", "public-allow-probe")
		}, NodeTimeout(time.Minute))
		apply(route("ntfy"))
		Eventually(ctx, func() bool { status, _ = accepted("ntfy"); return status == "True" }).
			WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "Gateway public refuses ntfy, which the public map lists")
	}, SpecTimeout(10*time.Minute))
})

// appCodes is each sandbox app's status code through the Gateway (0 when unreachable).
func appCodes(ctx context.Context, hosts map[string]string) map[string]int {
	codes := map[string]int{}
	for app, host := range hosts {
		codes[app] = 0
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host, nil)
		if err != nil {
			continue
		}
		resp, err := insecureClient.Do(req)
		if err != nil {
			continue
		}
		codes[app] = resp.StatusCode
		_ = resp.Body.Close()
	}
	return codes
}
