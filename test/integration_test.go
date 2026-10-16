package test

import (
	"crypto/tls"
	"fmt"
	"testing"
	"time"

	http_helper "github.com/gruntwork-io/terratest/modules/http-helper"
)

func TestArgoCDCheck(t *testing.T) {
	t.Parallel()

	// Wait until the Gateway accepted the Argo CD route
	host := routeHost(t, "argocd", "argocd-server")

	// Setup a TLS configuration to submit with the helper, a blank struct is acceptable
	tlsConfig := tls.Config{}

	// Test the endpoint for up to 5 minutes. This will only fail if we timeout waiting for the service to return a 200 response
	http_helper.HttpGetWithRetryWithCustomValidation(
		t,
		fmt.Sprintf("https://%s", host),
		&tlsConfig,
		30,
		30*time.Second,
		func(statusCode int, body string) bool {
			return statusCode == 200
		},
	)
}
