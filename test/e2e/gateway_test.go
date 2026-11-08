package e2e

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/routes"
	"github.com/duongle-steven/homelab/test/internal/testenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gatewaySandboxOnly(t *testing.T) { testenv.LocalOnly(t) }

// gateway is the part of a Gateway API Gateway the tests read.
type gateway struct {
	Status struct {
		Addresses []struct {
			Value string `json:"value"`
		} `json:"addresses"`
		Conditions []routes.Condition `json:"conditions"`
	} `json:"status"`
}

func TestGatewayProgrammed(t *testing.T) {
	gatewaySandboxOnly(t)
	// Both get an address: gateway from ServiceLB (the k3d node), public its ClusterIP
	for _, name := range []string{"gateway", "public"} {
		t.Run(name, func(t *testing.T) {
			var gw gateway
			require.Eventually(t, func() bool {
				gw = gateway{}
				return getResource(t, "istio-system", &gw, "gateway", name) && routes.ConditionsMet(gw.Status.Conditions, "Accepted", "Programmed")
			}, 20*time.Minute, 15*time.Second, "Gateway %s is not programmed", name)
			require.NotEmpty(t, gw.Status.Addresses)
			if name == "gateway" {
				// ServiceLB publishes it on the node the tests dial
				assert.Equal(t, gatewayAddress(t), gw.Status.Addresses[0].Value)
			}
		})
	}
}

func TestGatewayHTTPSRedirect(t *testing.T) {
	gatewaySandboxOnly(t)
	host := routeHost(t, "excalidraw", "excalidraw")
	resp, err := insecureClient.Get("http://" + host + "/some/path?x=1")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusMovedPermanently, resp.StatusCode)
	location, err := resp.Location()
	require.NoError(t, err)
	assert.Equal(t, "https", location.Scheme)
	assert.Equal(t, host, location.Hostname())
	// Some Gateways write the default port (:443) into Location; browsers treat both forms the same
	assert.Contains(t, []string{"", "443"}, location.Port())
	assert.Equal(t, "/some/path?x=1", location.RequestURI())
}

func TestGatewayNoIngress(t *testing.T) {
	gatewaySandboxOnly(t)
	// Operators remove the Ingresses they created earlier on their own schedule (kaniop waits for Kanidm)
	var ingresses struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	assert.Eventually(t, func() bool {
		ingresses.Items = nil
		return getResource(t, "default", &ingresses, "ingress", "--all-namespaces") && len(ingresses.Items) == 0
	}, 10*time.Minute, 15*time.Second, "every component uses Gateway API routes, left: %+v", ingresses.Items)
	var namespace struct{}
	assert.False(t, getResource(t, "default", &namespace, "namespace", "ingress-nginx"), "ingress-nginx must be gone")
}

func TestGatewayForgejoSSH(t *testing.T) {
	gatewaySandboxOnly(t)
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(gatewayAddress(t), "22"), 10*time.Second)
		if err != nil {
			return false
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		banner, err := bufio.NewReader(conn).ReadString('\n')
		return err == nil && strings.HasPrefix(banner, "SSH-2.0-")
	}, 20*time.Minute, 15*time.Second, "no SSH banner from forgejo on %s:22", gatewayAddress(t))
}

// TestGatewayZotLargePush uploads one 200 MiB blob through the Gateway, as an image push would.
func TestGatewayZotLargePush(t *testing.T) {
	gatewaySandboxOnly(t)
	base := "https://" + registryHost
	waitForApp(t, base+"/livez")
	password := generatedValue(t, "zot/generated/ci", "password")

	// Same content every run, so zot stores a single copy
	blob := make([]byte, 200<<20)
	rand.New(rand.NewSource(1)).Read(blob)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(blob))

	client := *insecureClient
	client.Timeout = 5 * time.Minute

	req, err := http.NewRequest("POST", base+"/v2/gateway-test/blobs/uploads/", nil)
	require.NoError(t, err)
	req.SetBasicAuth("ci", password)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	location, err := resp.Location()
	require.NoError(t, err)
	query := location.Query()
	query.Set("digest", digest)
	location.RawQuery = query.Encode()

	req, err = http.NewRequest("PUT", location.String(), bytes.NewReader(blob))
	require.NoError(t, err)
	req.SetBasicAuth("ci", password)
	req.Header.Set("Content-Type", "application/octet-stream")
	start := time.Now()
	resp, err = client.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "%s", body)
	t.Logf("pushed 200 MiB through the Gateway in %s", time.Since(start).Round(time.Millisecond))
}

// Gateway public admits routes only from the public map's namespaces (system/istio-system/values.yaml public): an
// admin app's route there must stay refused, a listed one accepted
func TestGatewayPublicAllowList(t *testing.T) {
	gatewaySandboxOnly(t)
	host := "public-allow-probe." + testenv.SuiteDomain(t)
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
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
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
		if !getResource(t, namespace, &r, "httproute", "public-allow-probe") {
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
	t.Cleanup(func() { _ = exec.Command("kubectl", "delete", "namespace", refused, "--wait=false").Run() })
	apply(route(refused))
	var status, reason string
	require.Eventually(t, func() bool { status, reason = accepted(refused); return status != "" }, 2*time.Minute, 5*time.Second,
		"Gateway public never reported on a route from %s", refused)
	assert.Equal(t, "False", status, "a namespace off the public map reaches Gateway public")
	assert.Equal(t, "NotAllowedByListeners", reason)

	t.Cleanup(func() {
		_ = exec.Command("kubectl", "--namespace", "ntfy", "delete", "httproute", "public-allow-probe").Run()
	})
	apply(route("ntfy"))
	require.Eventually(t, func() bool { status, _ = accepted("ntfy"); return status == "True" }, 2*time.Minute, 5*time.Second,
		"Gateway public refuses ntfy, which the public map lists")
}
