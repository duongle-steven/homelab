package e2e

import (
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/routes"
	"github.com/stretchr/testify/require"
)

// getResource decodes `kubectl get <args> --output json` into out and reports whether the command succeeded.
// It reads stdout only, so kubectl warnings cannot break the JSON.
func getResource(t *testing.T, namespace string, out any, args ...string) bool {
	args = append(append([]string{"--namespace", namespace, "get"}, args...), "--output", "json")
	raw, err := exec.Command("kubectl", args...).Output()
	if err != nil {
		return false
	}
	require.NoError(t, json.Unmarshal(raw, out))
	return true
}

// routeHost waits until the HTTPRoute is ready and returns its first hostname.
func routeHost(t *testing.T, namespace, name string) string {
	var route routes.HTTPRoute
	require.Eventually(t, func() bool {
		route = routes.HTTPRoute{}
		return getResource(t, namespace, &route, "httproute", name) && route.Ready()
	}, 30*time.Minute, 30*time.Second, "HTTPRoute %s/%s is not accepted with resolved backends", namespace, name)
	return route.Spec.Hostnames[0]
}
