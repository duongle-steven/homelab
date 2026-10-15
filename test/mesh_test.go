package test

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// meshOptOuts are the pods labelled istio.io/dataplane-mode=none, as namespace/app.kubernetes.io/name: the
// webhooks the API server calls (scripts/check-mesh-exclusions) and the VPN routers. Namespaces outside the mesh
// are listed in the root ApplicationSet's templatePatch (system/argocd/values.yaml), OUTSIDE in
// scripts/check-mesh-exclusions and TestMeshEnrolled; keep the three in step.
var meshOptOuts = []string{
	"cert-manager/webhook",
	"cert-manager/trust-manager",
	"external-secrets/external-secrets-webhook",
	"monitoring-system/kube-prometheus-stack-prometheus-operator",
	"cloudnative-pg/cloudnative-pg",
	"wireguard/wireguard",
	"tailscale/tailscale",
}

func optOutAllowed(namespace, name string) bool {
	for _, n := range meshOptOuts {
		if n == namespace+"/"+name {
			return true
		}
	}
	return false
}

func TestMeshOptOutAllowed(t *testing.T) {
	assert.True(t, optOutAllowed("cert-manager", "webhook"))
	assert.False(t, optOutAllowed("forgejo", "webhook"), "a generic name must not opt out in another namespace")
	assert.False(t, optOutAllowed("excalidraw", "excalidraw"))
}

func meshSandboxOnly(t *testing.T) {
	if os.Getenv("ENV") != "dev" {
		t.Skip("mesh tests run against the sandbox only")
	}
}

type meshPod struct {
	Metadata struct {
		Name        string            `json:"name"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		HostNetwork bool `json:"hostNetwork"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// TestMeshEnrolled checks every namespace Argo CD manages is in the mesh (istio-system out) and every running
// pod there is redirected to ztunnel, except the webhook pods, which opt out.
func TestMeshEnrolled(t *testing.T) {
	meshSandboxOnly(t)
	var apps struct {
		Items []struct {
			Spec struct {
				Destination struct {
					Namespace string `json:"namespace"`
				} `json:"destination"`
			} `json:"spec"`
		} `json:"items"`
	}
	require.True(t, getResource(t, "argocd", &apps, "applications"))
	namespaces := map[string]bool{}
	for _, a := range apps.Items {
		namespaces[a.Spec.Destination.Namespace] = true
	}
	require.NotEmpty(t, namespaces)

	for ns := range namespaces {
		t.Run(ns, func(t *testing.T) {
			var namespace struct {
				Metadata struct {
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
			}
			require.True(t, getResource(t, "default", &namespace, "namespace", ns))
			// Outside the mesh: keep in step with the root ApplicationSet's templatePatch and scripts/check-mesh-exclusions
			want := map[bool]string{true: "none", false: "ambient"}[ns == "istio-system"]
			require.Equal(t, want, namespace.Metadata.Labels["istio.io/dataplane-mode"])
			if want == "none" {
				return
			}
			// istio-cni enrols running pods when their namespace gets the label; on a fresh cluster that can lag the sync
			assert.Eventually(t, func() bool {
				var pods struct {
					Items []meshPod `json:"items"`
				}
				if !getResource(t, ns, &pods, "pods") {
					return false
				}
				for _, p := range pods.Items {
					if p.Spec.HostNetwork || p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed" {
						continue
					}
					if p.Metadata.Labels["istio.io/dataplane-mode"] == "none" {
						if !optOutAllowed(ns, p.Metadata.Labels["app.kubernetes.io/name"]) {
							t.Logf("%s/%s opts out of the mesh but is not a known webhook pod", ns, p.Metadata.Name)
							return false
						}
						continue
					}
					if p.Metadata.Annotations["ambient.istio.io/redirection"] != "enabled" {
						t.Logf("%s/%s is not redirected to ztunnel", ns, p.Metadata.Name)
						return false
					}
				}
				return true
			}, 5*time.Minute, 15*time.Second, "pods in %s are not all in the mesh", ns)
		})
	}
}

// meshCurlIn runs curl from a temporary pod in a namespace (namespace default is outside the mesh unless labels
// enroll the pod) and returns the HTTP status it printed ("000" when the connection failed).
func meshCurlIn(t *testing.T, namespace, name, target string, labels string) string {
	options := k8s.NewKubectlOptions("", "", namespace)
	options.Logger = logger.Discard
	// A pod left over from an interrupted run would make kubectl run fail with AlreadyExists
	_, _ = k8s.RunKubectlAndGetOutputE(t, options, "delete", "pod", name, "--ignore-not-found", "--wait")
	args := []string{"run", name, "--rm", "-i", "--restart=Never", "--image=curlimages/curl:8.23.0"}
	if labels != "" {
		args = append(args, "--labels="+labels)
	}
	// Give istio-cni a moment to redirect the new pod before it connects
	args = append(args, "--", "sh", "-c", `sleep 3; curl -s -m 10 -o /dev/null -w "%{http_code}\n" `+target+` || true`)
	out, _ := k8s.RunKubectlAndGetOutputE(t, options, args...)
	status := regexp.MustCompile(`(?m)^[0-9]{3}$`).FindString(out)
	require.NotEmpty(t, status, "no HTTP status from %s:\n%s", name, out)
	return status
}

// TestMeshRejectsPlaintext proves strict mTLS and the default deny: a pod outside the mesh is refused, and so is a
// pod in the mesh that no allow policy names, after ztunnel authenticated it.
func TestMeshRejectsPlaintext(t *testing.T) {
	meshSandboxOnly(t)
	target := "http://excalidraw.excalidraw"
	assert.Equal(t, "000", meshCurlIn(t, "default", "mesh-plain", target, ""), "a pod outside the mesh reached a meshed service in plaintext")
	// In the mesh but not allowed into excalidraw (default deny): refused after the mTLS handshake
	assert.Equal(t, "000", meshCurlIn(t, "default", "mesh-member", target, "istio.io/dataplane-mode=ambient"))

	options := k8s.NewKubectlOptions("", "", "istio-system")
	options.Logger = logger.Discard
	// Every node's ztunnel (the connections may have gone through any of them); a selector alone keeps only 10 lines
	logs, err := k8s.RunKubectlAndGetOutputE(t, options, "logs", "--selector=app=ztunnel", "--prefix", "--since=5m", "--tail=-1")
	require.NoError(t, err)
	assert.Contains(t, logs, "policy rejection", "ztunnel did not log the refused plaintext connection")
	assert.Contains(t, logs, `src.identity="spiffe://cluster.local/ns/default/sa/default"`,
		"ztunnel did not authenticate the meshed pod (mTLS)")
	assert.Regexp(t, `src.identity="spiffe://cluster.local/ns/default/sa/default"[^\n]*policy rejection|policy rejection[^\n]*src.identity="spiffe://cluster.local/ns/default/sa/default"`, logs,
		"ztunnel did not refuse the meshed pod by policy")
}

// TestMeshAllowsSameNamespace proves the `allow` policies' same-namespace rule: a meshed pod inside excalidraw
// reaches excalidraw.
func TestMeshAllowsSameNamespace(t *testing.T) {
	meshSandboxOnly(t)
	assert.Equal(t, "200", meshCurlIn(t, "excalidraw", "mesh-neighbour", "http://excalidraw.excalidraw", ""))
}

// TestMeshPrometheusTargetsUp proves the allows for Prometheus: no scrape target is down.
func TestMeshPrometheusTargetsUp(t *testing.T) {
	meshSandboxOnly(t)
	options := k8s.NewKubectlOptions("", "", "monitoring-system")
	options.Logger = logger.Discard
	query := "/api/v1/namespaces/monitoring-system/services/monitoring-system-kube-pro-prometheus:9090/proxy/api/v1/query?query=up%3D%3D0"
	assert.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "--raw", query)
		if err != nil {
			return false
		}
		var res struct {
			Data struct {
				Result []struct {
					Metric map[string]string `json:"metric"`
				} `json:"result"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(out), &res) != nil {
			return false
		}
		for _, r := range res.Data.Result {
			t.Logf("target down: %v", r.Metric)
		}
		return len(res.Data.Result) == 0
	}, 5*time.Minute, 30*time.Second, "Prometheus has targets down")
}
