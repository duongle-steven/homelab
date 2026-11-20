package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/scopes"
	"github.com/duongle-steven/homelab/test/internal/smoke"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// wantedGroups are the deploy groups just dev chose (HOMELAB_SANDBOX_GROUPS, space-separated): one of them missing
// fails a spec instead of skipping it
func wantedGroups() []string { return strings.Fields(os.Getenv("HOMELAB_SANDBOX_GROUPS")) }

// scopeMap is test/scopes.yaml's core and groups
func scopeMap() scopes.Map {
	GinkgoHelper()
	m, err := scopes.Load()
	Expect(err).NotTo(HaveOccurred())
	return m
}

// groupDeployed reports whether the sandbox runs a deploy group (the cluster Secret's homelab/group-<name>; always
// outside the sandbox); it fails the spec when smoke.GroupState says so
func groupDeployed(ctx context.Context, group string) bool {
	if suiteEnv() != "dev" {
		return true
	}
	// A single API error (a busy API server) is not an answer: retried for a minute
	var out string
	var err error
	for try := 0; try < 6; try++ {
		if out, err = kube.Kubectl(ctx, "argocd", "get", "secret", "in-cluster", "-o", `jsonpath={.metadata.labels.homelab/group-`+group+`}`); err == nil {
			break
		}
		sleep(ctx, 10*time.Second)
	}
	skip, failure := smoke.GroupState(group, wantedGroups(), strings.TrimSpace(out) == "true", err)
	if failure != "" {
		Fail(failure)
	}
	return !skip
}

// skipUnlessGroups skips a spec whose groups the sandbox does not run now
func skipUnlessGroups(ctx context.Context, groups []string) {
	for _, g := range groups {
		if !groupDeployed(ctx, g) {
			Skip(fmt.Sprintf("needs the sandbox group %s, not deployed (just dev deploys what the specs it selects need; --apps %s)", g, g))
		}
	}
}

// appDeployed reports whether the app (or component) in namespace ns runs: always when it is the core's
func appDeployed(ctx context.Context, ns string) bool {
	g := scopeMap().GroupOfNamespace(ns)
	return g == "" || groupDeployed(ctx, g)
}

// skipUnlessDeployed skips a spec of an app (or component) the sandbox does not run now
func skipUnlessDeployed(ctx context.Context, ns string) {
	if g := scopeMap().GroupOfNamespace(ns); g != "" {
		skipUnlessGroups(ctx, []string{g})
	}
}

// deployedSmokeApps is smoke.Apps without those whose group the sandbox does not run
func deployedSmokeApps(ctx context.Context, env string) []smoke.App {
	var apps []smoke.App
	for _, a := range smoke.Apps(env) {
		if appDeployed(ctx, a.Namespace) {
			apps = append(apps, a)
		}
	}
	return apps
}

var _ = Describe("Smoke", Label("sandbox", "testbed", "production", "smoke"), func() {
	for _, app := range smoke.Apps(os.Getenv("ENV")) {
		It(app.Name, func(ctx SpecContext) {
			skipUnlessDeployed(ctx, app.Namespace)

			// Wait until the Gateway accepted the route with a resolved backend
			host := routeHost(ctx, app.Namespace, app.Name)

			// Setup a TLS configuration, ignore the certificate because we may not use cert-manager (like the sandbox environment)
			tlsConfig := tls.Config{
				InsecureSkipVerify: os.Getenv("INSECURE_SKIP_VERIFY") != "",
			}

			// The endpoint must answer 200 within 30 minutes; sandbox hosts are dialled on the node (dialGateway)
			client := &http.Client{
				Timeout:   30 * time.Second,
				Transport: &http.Transport{TLSClientConfig: &tlsConfig, DialContext: dialGateway},
			}
			url := fmt.Sprintf("https://%s", host)
			Eventually(ctx, func() error {
				return getOK(ctx, client, url)
			}).WithTimeout(30*time.Minute).WithPolling(time.Minute).Should(Succeed(), "GET %s", url)
		}, SpecTimeout(65*time.Minute))
	}
})

// getOK fails unless GET url answers 200
func getOK(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
