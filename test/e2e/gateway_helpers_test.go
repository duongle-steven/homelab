package e2e

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/routes"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/require"
)

// getResource decodes `kubectl get <args> --output json` into out and reports whether the command succeeded.
// It reads stdout only, so kubectl warnings cannot break the JSON.
func getResource(ctx context.Context, namespace string, out any, args ...string) bool {
	return kube.GetJSON(ctx, namespace, out, args...) == nil
}

// routeHost waits until the HTTPRoute is ready and returns its first hostname.
func routeHost(ctx context.Context, namespace, name string) string {
	var route routes.HTTPRoute
	Eventually(ctx, func() bool {
		route = routes.HTTPRoute{}
		return getResource(ctx, namespace, &route, "httproute", name) && route.Ready()
	}).WithTimeout(30*time.Minute).WithPolling(30*time.Second).Should(BeTrue(),
		"HTTPRoute %s/%s is not accepted with resolved backends", namespace, name)
	return route.Spec.Hostnames[0]
}

// legacyGetResource is getResource for the tests not on Ginkgo yet (14f-2 removes it).
func legacyGetResource(t *testing.T, namespace string, out any, args ...string) bool {
	args = append(append([]string{"--namespace", namespace, "get"}, args...), "--output", "json")
	raw, err := exec.Command("kubectl", args...).Output()
	if err != nil {
		return false
	}
	require.NoError(t, json.Unmarshal(raw, out))
	return true
}

// legacyRouteHost is routeHost for the tests not on Ginkgo yet (14f-2 removes it).
func legacyRouteHost(t *testing.T, namespace, name string) string {
	var route routes.HTTPRoute
	require.Eventually(t, func() bool {
		route = routes.HTTPRoute{}
		return legacyGetResource(t, namespace, &route, "httproute", name) && route.Ready()
	}, 30*time.Minute, 30*time.Second, "HTTPRoute %s/%s is not accepted with resolved backends", namespace, name)
	return route.Spec.Hostnames[0]
}

// kubectlJSON decodes `kubectl <args> -o json` (any namespace in args) into out, or fails the spec
func kubectlJSON(ctx context.Context, out any, args ...string) {
	GinkgoHelper()
	raw, err := kube.Kubectl(ctx, "", append(args, "-o", "json")...)
	Expect(err).NotTo(HaveOccurred(), raw)
	Expect(json.Unmarshal([]byte(raw), out)).To(Succeed())
}

// waitForDeployment waits up to 10 minutes until the Deployment reports Available, as terratest's
// WaitUntilDeploymentAvailable did with 60 tries 10 s apart
func waitForDeployment(ctx context.Context, namespace, name string) {
	GinkgoHelper()
	Eventually(ctx, func() (string, error) {
		return kube.Kubectl(ctx, namespace, "get", "deployment", name, "-o", `jsonpath={.status.conditions[?(@.type=="Available")].status}`)
	}).WithTimeout(10*time.Minute).WithPolling(10*time.Second).Should(Equal("True"), "Deployment %s/%s is not available", namespace, name)
}

// sleep waits d or until the spec's context ends
func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
