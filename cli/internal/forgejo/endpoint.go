package forgejo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Endpoint is how lab reaches one environment's Forgejo: its URL, an HTTP client for the API and git's extra
// configuration (git -c values).
type Endpoint struct {
	URL  string
	HTTP *http.Client
	Git  []string
}

// ProductionEndpoint is Forgejo on the LAN Gateway, https://git.<domain>.
func ProductionEndpoint(domain string) Endpoint {
	return Endpoint{URL: "https://git." + domain, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// RepositoryURL is ops/homelab's clone URL.
func (ep Endpoint) RepositoryURL() string { return ep.URL + "/" + Org + "/" + Repo + ".git" }

// SandboxEndpoint reaches the sandbox's Forgejo on the k3d node (as the tests do: GitHub Actions runs no localhost
// proxy) and trusts the sandbox CA. The cleanup removes the CA file git reads.
func SandboxEndpoint(domain, node string, caPEM []byte) (Endpoint, func(), error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return Endpoint{}, nil, fmt.Errorf("the sandbox CA is not a PEM certificate")
	}
	file, err := os.CreateTemp("", "lab-sandbox-ca-")
	if err != nil {
		return Endpoint{}, nil, err
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	if _, err := file.Write(caPEM); err != nil {
		_ = file.Close()
		cleanup()
		return Endpoint{}, nil, err
	}
	_ = file.Close()
	host := "git." + domain
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// git. and the sandbox's other hosts (ci.: lab's rotations) answer on the node's Gateway
			if h, port, err := net.SplitHostPort(addr); err == nil && (h == host || strings.HasSuffix(h, "."+domain)) {
				addr = net.JoinHostPort(node, port)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	return Endpoint{
		URL:  "https://" + host,
		HTTP: &http.Client{Timeout: 30 * time.Second, Transport: transport},
		Git:  []string{"http.curloptResolve=" + host + ":443:" + node, "http.sslCAInfo=" + file.Name()},
	}, cleanup, nil
}
