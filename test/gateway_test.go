package test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayRouteReady(t *testing.T) {
	both := `{"type":"Accepted","status":"True"},{"type":"ResolvedRefs","status":"True"}`
	cases := map[string]struct {
		route string
		want  bool
	}{
		"accepted with resolved backends": {`{"spec":{"hostnames":["a.example"]},"status":{"parents":[{"conditions":[` + both + `]}]}}`, true},
		"not reconciled yet":              {`{"spec":{"hostnames":["a.example"]}}`, false},
		"backend missing":                 {`{"spec":{"hostnames":["a.example"]},"status":{"parents":[{"conditions":[{"type":"Accepted","status":"True"},{"type":"ResolvedRefs","status":"False"}]}]}}`, false},
		"one parent refused":              {`{"spec":{"hostnames":["a.example"]},"status":{"parents":[{"conditions":[` + both + `]},{"conditions":[{"type":"Accepted","status":"False"},{"type":"ResolvedRefs","status":"True"}]}]}}`, false},
		"no hostname":                     {`{"status":{"parents":[{"conditions":[` + both + `]}]}}`, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var r httpRoute
			require.NoError(t, json.Unmarshal([]byte(c.route), &r))
			assert.Equal(t, c.want, r.ready())
		})
	}
}

func gatewaySandboxOnly(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("Gateway tests run against the sandbox only")
	}
}

// gateway is the part of a Gateway API Gateway the tests read.
type gateway struct {
	Status struct {
		Addresses []struct {
			Value string `json:"value"`
		} `json:"addresses"`
		Conditions []condition `json:"conditions"`
	} `json:"status"`
}

func TestGatewayProgrammed(t *testing.T) {
	gatewaySandboxOnly(t)
	// Both are pinned (system/gateway/values-dev.yaml); the localhost proxy forwards to homelab
	addresses := map[string]string{"homelab": sandboxIngress, "homelab-public": "172.30.255.201"}
	for name, address := range addresses {
		t.Run(name, func(t *testing.T) {
			var gw gateway
			require.Eventually(t, func() bool {
				gw = gateway{}
				return getResource(t, "gateway", &gw, "gateway", name) && conditionsMet(gw.Status.Conditions, "Accepted", "Programmed")
			}, 20*time.Minute, 15*time.Second, "Gateway %s is not programmed", name)
			require.NotEmpty(t, gw.Status.Addresses)
			assert.Equal(t, address, gw.Status.Addresses[0].Value)
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
	assert.Equal(t, "https://"+host+"/some/path?x=1", resp.Header.Get("Location"))
}

func TestGatewayNoIngress(t *testing.T) {
	gatewaySandboxOnly(t)
	var ingresses struct {
		Items []any `json:"items"`
	}
	require.True(t, getResource(t, "default", &ingresses, "ingress", "--all-namespaces"))
	assert.Empty(t, ingresses.Items, "every component uses Gateway API routes")
	var namespace struct{}
	assert.False(t, getResource(t, "default", &namespace, "namespace", "ingress-nginx"), "ingress-nginx must be gone")
}

func TestGatewayGiteaSSH(t *testing.T) {
	gatewaySandboxOnly(t)
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(sandboxIngress, "22"), 10*time.Second)
		if err != nil {
			return false
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		banner, err := bufio.NewReader(conn).ReadString('\n')
		return err == nil && strings.HasPrefix(banner, "SSH-2.0-")
	}, 20*time.Minute, 15*time.Second, "no SSH banner from gitea on %s:22", sandboxIngress)
}

// TestGatewayZotLargePush uploads one 200 MiB blob through the Gateway, as an image push would.
func TestGatewayZotLargePush(t *testing.T) {
	gatewaySandboxOnly(t)
	base := "https://" + registryHost
	waitForApp(t, base+"/livez")
	password := secretValue(t, "global-secrets", "registry.ci", "password")

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
