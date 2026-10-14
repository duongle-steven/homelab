package test

import (
	"crypto/tls"
	"fmt"
	"os"
	"testing"
	"time"

	http_helper "github.com/gruntwork-io/terratest/modules/http-helper"
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

func TestSmoke(t *testing.T) {
	t.Parallel()

	for _, app := range smokeApps(os.Getenv("ENV")) {
		app := app // https://github.com/golang/go/wiki/CommonMistakes#using-goroutines-on-loop-iterator-variables
		t.Run(app.name, func(t *testing.T) {
			t.Parallel()

			// Wait until the Gateway accepted the route with a resolved backend
			host := routeHost(t, app.namespace, app.name)

			// Setup a TLS configuration, ignore the certificate because we may not use cert-manager (like the sandbox environment)
			tlsConfig := tls.Config{
				InsecureSkipVerify: os.Getenv("INSECURE_SKIP_VERIFY") != "",
			}

			// Test the endpoint, this will only fail if we timeout waiting for the service to return a 200 response
			http_helper.HttpGetWithRetryWithCustomValidation(
				t,
				fmt.Sprintf("https://%s", host),
				&tlsConfig,
				30,
				60*time.Second,
				func(statusCode int, body string) bool {
					return statusCode == 200
				},
			)
		})
	}
}
