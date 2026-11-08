package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/smoke"
	"github.com/gruntwork-io/terratest/modules/k8s"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/require"
)

// optionalApps are deployed in the sandbox only when just dev asks (the justfile's optional_apps,
// docs/superpowers/specs/2026-10-28-sandbox-optional-apps-design.md); always in production and the release sandbox
var optionalApps = map[string]bool{"paperless": true, "opencloud": true}

// wantedApps are the optional apps just dev deployed (HOMELAB_SANDBOX_APPS, space-separated)
func wantedApps() []string { return strings.Fields(os.Getenv("HOMELAB_SANDBOX_APPS")) }

// appPresent reports whether the optional app's Application exists and is not being deleted
func appPresent(ctx context.Context, app string) (bool, error) {
	out, err := kube.Kubectl(ctx, "argocd", "get", "applications.argoproj.io", app,
		"--ignore-not-found", "-o", "jsonpath={.metadata.name}{.metadata.deletionTimestamp}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == app, nil
}

// appDeployed reports whether an app is deployed: an optional one only when its Application exists; it fails the
// spec when smoke.OptionalAppState says so
func appDeployed(ctx context.Context, app string) bool {
	if !optionalApps[app] {
		return true
	}
	present, err := appPresent(ctx, app)
	skip, failure := smoke.OptionalAppState(app, wantedApps(), present, err)
	if failure != "" {
		Fail(failure)
	}
	return !skip
}

// skipUnlessDeployed skips a spec of an optional app the sandbox does not run now
func skipUnlessDeployed(ctx context.Context, app string) {
	if !appDeployed(ctx, app) {
		Skip(fmt.Sprintf("%s is not deployed (optional in the sandbox: just dev --apps %s)", app, app))
	}
}

// deployedSmokeApps is smoke.Apps without the optional apps that are not deployed
func deployedSmokeApps(ctx context.Context, env string) []smoke.App {
	var apps []smoke.App
	for _, a := range smoke.Apps(env) {
		if appDeployed(ctx, a.Namespace) {
			apps = append(apps, a)
		}
	}
	return apps
}

// legacyAppDeployed is appDeployed for the tests not on Ginkgo yet (14f-2 removes it).
func legacyAppDeployed(t *testing.T, app string) bool {
	if !optionalApps[app] {
		return true
	}
	out, err := k8s.RunKubectlAndGetOutputE(t, k8s.NewKubectlOptions("", "", "argocd"), "get", "applications.argoproj.io", app,
		"--ignore-not-found", "-o", "jsonpath={.metadata.name}{.metadata.deletionTimestamp}")
	skip, failure := smoke.OptionalAppState(app, wantedApps(), err == nil && strings.TrimSpace(out) == app, err)
	if failure != "" {
		require.FailNow(t, failure)
	}
	return !skip
}

// legacySkipUnlessDeployed is skipUnlessDeployed for the tests not on Ginkgo yet (14f-2 removes it).
func legacySkipUnlessDeployed(t *testing.T, app string) {
	if !legacyAppDeployed(t, app) {
		t.Skipf("%s is not deployed (optional in the sandbox: just dev --apps %s)", app, app)
	}
}

// legacyDeployedSmokeApps is deployedSmokeApps for the tests not on Ginkgo yet (14f-2 removes it).
func legacyDeployedSmokeApps(t *testing.T, env string) []smoke.App {
	var apps []smoke.App
	for _, a := range smoke.Apps(env) {
		if legacyAppDeployed(t, a.Namespace) {
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
