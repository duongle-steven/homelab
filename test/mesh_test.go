package test

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// meshOptOuts are the pods the API server calls as webhooks (scripts/check-mesh-exclusions), by
// app.kubernetes.io/name; the mesh's strict mTLS would refuse the API server's plaintext calls.
var meshOptOuts = []string{"webhook", "trust-manager", "external-secrets-webhook", "kube-prometheus-stack-prometheus-operator", "cloudnative-pg", "wireguard", "tailscale"}

func optOutAllowed(name string) bool {
	for _, n := range meshOptOuts {
		if n == name {
			return true
		}
	}
	return false
}

func TestMeshOptOutAllowed(t *testing.T) {
	assert.True(t, optOutAllowed("trust-manager"))
	assert.False(t, optOutAllowed("excalidraw"))
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
			want := map[bool]string{true: "none", false: "ambient"}[ns == "istio-system"]
			require.Equal(t, want, namespace.Metadata.Labels["istio.io/dataplane-mode"])
			if want == "none" {
				return
			}
			// Pods created before the namespace got its label are redirected only after a restart
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
						if !optOutAllowed(p.Metadata.Labels["app.kubernetes.io/name"]) {
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

// meshCurl runs curl from a temporary pod in namespace default (outside the mesh unless labels enroll the pod)
// and returns the HTTP status it printed ("000" when the connection failed).
func meshCurl(t *testing.T, name, target string, labels string) string {
	options := k8s.NewKubectlOptions("", "", "default")
	options.Logger = logger.Discard
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

// TestMeshRejectsPlaintext proves strict mTLS: a pod outside the mesh is refused, a pod in the mesh gets through,
// and ztunnel records both identities of the mTLS connection.
func TestMeshRejectsPlaintext(t *testing.T) {
	meshSandboxOnly(t)
	target := "http://excalidraw.excalidraw"
	assert.Equal(t, "000", meshCurl(t, "mesh-plain", target, ""), "a pod outside the mesh reached a meshed service in plaintext")
	assert.Equal(t, "200", meshCurl(t, "mesh-member", target, "istio.io/dataplane-mode=ambient"))

	options := k8s.NewKubectlOptions("", "", "istio-system")
	options.Logger = logger.Discard
	logs, err := k8s.RunKubectlAndGetOutputE(t, options, "logs", "daemonset/ztunnel", "--since=5m")
	require.NoError(t, err)
	assert.Contains(t, logs, "policy rejection", "ztunnel did not log the refused plaintext connection")
	assert.True(t, strings.Contains(logs, `src.identity="spiffe://cluster.local/ns/default/sa/default"`) &&
		strings.Contains(logs, `dst.identity="spiffe://cluster.local/ns/excalidraw/sa/`),
		"ztunnel did not log the mTLS connection from default to excalidraw")
}
