package e2e

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ArgoCDCheck", Label("production", "argocd"), func() {
	It("answers 200", func(ctx SpecContext) {
		// Wait until the Gateway accepted the Argo CD route
		host := routeHost(ctx, "argocd", "argocd-server")

		// Production's certificate is verified
		client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{}}}

		// Test the endpoint for up to 15 minutes (30 tries, 30 s apart): it must return a 200 response
		url := fmt.Sprintf("https://%s", host)
		Eventually(ctx, func() error {
			return getOK(ctx, client, url)
		}).WithTimeout(15*time.Minute).WithPolling(30*time.Second).Should(Succeed(), "GET %s", url)
	}, SpecTimeout(50*time.Minute))
})
