package test

import (
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// condition is the part of a Kubernetes status condition the tests read.
type condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

// conditionsMet reports whether every wanted condition type has status True.
func conditionsMet(conditions []condition, types ...string) bool {
	status := map[string]string{}
	for _, c := range conditions {
		status[c.Type] = c.Status
	}
	for _, t := range types {
		if status[t] != "True" {
			return false
		}
	}
	return true
}

// httpRoute is the part of a Gateway API HTTPRoute the tests read.
type httpRoute struct {
	Spec struct {
		Hostnames []string `json:"hostnames"`
	} `json:"spec"`
	Status struct {
		Parents []struct {
			Conditions []condition `json:"conditions"`
		} `json:"parents"`
	} `json:"status"`
}

// ready reports whether the route has a hostname and every parent Gateway accepted it with resolved backends.
func (r httpRoute) ready() bool {
	if len(r.Spec.Hostnames) == 0 || len(r.Status.Parents) == 0 {
		return false
	}
	for _, p := range r.Status.Parents {
		if !conditionsMet(p.Conditions, "Accepted", "ResolvedRefs") {
			return false
		}
	}
	return true
}

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
	var route httpRoute
	require.Eventually(t, func() bool {
		route = httpRoute{}
		return getResource(t, namespace, &route, "httproute", name) && route.ready()
	}, 30*time.Minute, 30*time.Second, "HTTPRoute %s/%s is not accepted with resolved backends", namespace, name)
	return route.Spec.Hostnames[0]
}
