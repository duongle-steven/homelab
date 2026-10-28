package test

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
)

type smokeApp struct {
	name      string
	namespace string
}

// smokeApps returns the apps whose HTTPRoute is checked; the sandbox only runs a subset.
func smokeApps(env string) []smokeApp {
	if env == "dev" {
		return []smokeApp{
			{"argocd-server", "argocd"},
			{"excalidraw", "excalidraw"},
			{"kanidm", "kanidm"},
			{"paperless", "paperless"},
			{"grafana", "grafana"},
			{"zot", "zot"},
			{"actualbudget", "actualbudget"},
			{"homepage", "homepage"},
			{"pairdrop", "pairdrop"},
			{"speedtest", "speedtest"},
			{"forgejo", "forgejo"},
			{"opencloud", "opencloud"},
			{"ntfy", "ntfy"},
		}
	}

	return []smokeApp{
		{"argocd-server", "argocd"},
		{"forgejo", "forgejo"},
		{"grafana", "grafana"},
		{"homepage", "homepage"},
		{"kanidm", "kanidm"},
		{"zot", "zot"},
	}
}

// optionalApps are deployed in the sandbox only when make dev asks (Makefile's optional_apps,
// docs/superpowers/specs/2026-10-28-sandbox-optional-apps-design.md); always in production and the release sandbox
var optionalApps = map[string]bool{"paperless": true, "opencloud": true}

// appDeployed reports whether an app is deployed: an optional one only when its Application exists
func appDeployed(t *testing.T, app string) bool {
	if !optionalApps[app] {
		return true
	}
	_, err := k8s.RunKubectlAndGetOutputE(t, k8s.NewKubectlOptions("", "", "argocd"), "get", "applications.argoproj.io", app)
	return err == nil
}

// skipUnlessDeployed skips a test of an optional app the sandbox does not run now
func skipUnlessDeployed(t *testing.T, app string) {
	if !appDeployed(t, app) {
		t.Skipf("%s is not deployed (optional in the sandbox: make dev apps=%s)", app, app)
	}
}

// deployedSmokeApps is smokeApps without the optional apps that are not deployed
func deployedSmokeApps(t *testing.T, env string) []smokeApp {
	var apps []smokeApp
	for _, a := range smokeApps(env) {
		if appDeployed(t, a.namespace) {
			apps = append(apps, a)
		}
	}
	return apps
}

func TestSmoke(t *testing.T) {
	t.Parallel()

	for _, app := range smokeApps(os.Getenv("ENV")) {
		app := app // https://github.com/golang/go/wiki/CommonMistakes#using-goroutines-on-loop-iterator-variables
		t.Run(app.name, func(t *testing.T) {
			t.Parallel()
			skipUnlessDeployed(t, app.namespace)

			// Wait until the Gateway accepted the route with a resolved backend
			host := routeHost(t, app.namespace, app.name)

			// Setup a TLS configuration, ignore the certificate because we may not use cert-manager (like the sandbox environment)
			tlsConfig := tls.Config{
				InsecureSkipVerify: os.Getenv("INSECURE_SKIP_VERIFY") != "",
			}

			// The endpoint must answer 200 within 30 minutes; sandbox hosts are dialled on the node (dialSandbox)
			client := &http.Client{
				Timeout:   30 * time.Second,
				Transport: &http.Transport{TLSClientConfig: &tlsConfig, DialContext: dialSandbox},
			}
			url := fmt.Sprintf("https://%s", host)
			for attempt := 1; ; attempt++ {
				resp, err := client.Get(url)
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						return
					}
					err = fmt.Errorf("status %d", resp.StatusCode)
				}
				if attempt == 30 {
					t.Fatalf("GET %s: %v", url, err)
				}
				t.Logf("GET %s: %v; retrying in a minute", url, err)
				time.Sleep(time.Minute)
			}
		})
	}
}
