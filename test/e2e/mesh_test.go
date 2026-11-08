package e2e

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// meshOptOuts are the pods labelled istio.io/dataplane-mode=none, as namespace/app.kubernetes.io/name: the
// webhooks the API server calls (scripts/check-mesh-policies) and the VPN routers. Namespaces outside the mesh
// are listed in the root ApplicationSet's templatePatch (system/argocd/values.yaml), OUTSIDE in
// scripts/check-mesh-policies and MeshEnrolled; keep the three in step.
var meshOptOuts = []string{
	"cert-manager/webhook",
	"cert-manager/trust-manager",
	"external-secrets/external-secrets-webhook",
	"monitoring-system/kube-prometheus-stack-prometheus-operator",
	"cloudnative-pg/cloudnative-pg",
	"kyverno/kyverno-admission-controller",
	"kyverno/kyverno-cleanup-controller", // production only: the sandbox runs no cleanup controller
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

var _ = Describe("MeshOptOutAllowed", Label("sandbox", "testbed", "production", "mesh"), func() {
	It("allows only the listed pods to opt out", func() {
		Expect(optOutAllowed("cert-manager", "webhook")).To(BeTrue())
		Expect(optOutAllowed("forgejo", "webhook")).To(BeFalse(), "a generic name must not opt out in another namespace")
		Expect(optOutAllowed("excalidraw", "excalidraw")).To(BeFalse())
	})
})

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

// MeshEnrolled checks every namespace Argo CD manages is in the mesh (istio-system out) and every running pod there
// is redirected to ztunnel, except the webhook pods, which opt out.
var _ = Describe("MeshEnrolled", Label("sandbox", "testbed", "mesh"), func() {
	It("enrols every managed namespace and its pods", func(ctx SpecContext) {
		var apps struct {
			Items []struct {
				Spec struct {
					Destination struct {
						Namespace string `json:"namespace"`
					} `json:"destination"`
				} `json:"spec"`
			} `json:"items"`
		}
		Expect(getResource(ctx, "argocd", &apps, "applications")).To(BeTrue())
		namespaces := map[string]bool{}
		for _, a := range apps.Items {
			namespaces[a.Spec.Destination.Namespace] = true
		}
		Expect(namespaces).NotTo(BeEmpty())

		for ns := range namespaces {
			By(ns)
			var namespace struct {
				Metadata struct {
					Labels map[string]string `json:"labels"`
				} `json:"metadata"`
			}
			Expect(getResource(ctx, "default", &namespace, "namespace", ns)).To(BeTrue())
			// Outside the mesh: keep in step with the root ApplicationSet's templatePatch and scripts/check-mesh-policies
			want := map[bool]string{true: "none", false: "ambient"}[ns == "istio-system"]
			Expect(namespace.Metadata.Labels["istio.io/dataplane-mode"]).To(Equal(want), ns)
			if want == "none" {
				continue
			}
			// istio-cni enrols running pods when their namespace gets the label; on a fresh cluster that can lag the sync
			Eventually(ctx, func() bool {
				var pods struct {
					Items []meshPod `json:"items"`
				}
				if !getResource(ctx, ns, &pods, "pods") {
					return false
				}
				for _, p := range pods.Items {
					if p.Spec.HostNetwork || p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed" {
						continue
					}
					if p.Metadata.Labels["istio.io/dataplane-mode"] == "none" {
						if !optOutAllowed(ns, p.Metadata.Labels["app.kubernetes.io/name"]) {
							GinkgoWriter.Printf("%s/%s opts out of the mesh but is not a known webhook pod\n", ns, p.Metadata.Name)
							return false
						}
						continue
					}
					if p.Metadata.Annotations["ambient.istio.io/redirection"] != "enabled" {
						GinkgoWriter.Printf("%s/%s is not redirected to ztunnel\n", ns, p.Metadata.Name)
						return false
					}
				}
				return true
			}).WithTimeout(5*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), "pods in %s are not all in the mesh", ns)
		}
	}, SpecTimeout(30*time.Minute))
})

// meshCurlIn runs curl from a temporary pod in a namespace (namespace default is outside the mesh unless labels
// enroll the pod) and returns the HTTP status it printed ("000" when the connection failed; certificates are not
// verified).
func meshCurlIn(ctx context.Context, namespace, name, target string, labels string) string {
	return curlFrom(ctx, namespace, name, target, labels, 3)
}

// curlFrom is meshCurlIn with the seconds the pod waits before connecting (istio-cni and the network policy
// controller set up a new pod asynchronously)
func curlFrom(ctx context.Context, namespace, name, target string, labels string, wait int) string {
	// A pod left over from an interrupted run would make kubectl run fail with AlreadyExists
	_, _ = kube.Kubectl(ctx, namespace, "delete", "pod", name, "--ignore-not-found", "--wait")
	// A slow image pull must not fail the probe (kubectl run waits 1 minute by default)
	args := []string{"run", name, "--rm", "-i", "--restart=Never", "--pod-running-timeout=5m", "--image=curlimages/curl:8.23.0"}
	if labels != "" {
		args = append(args, "--labels="+labels)
	}
	args = append(args, "--", "sh", "-c", fmt.Sprintf("sleep %d; ", wait)+`curl -sk -m 10 -o /dev/null -w "%{http_code}\n" `+target+` || true`)
	out, _ := kube.Kubectl(ctx, namespace, args...)
	status := regexp.MustCompile(`(?m)^[0-9]{3}$`).FindString(out)
	Expect(status).NotTo(BeEmpty(), "no HTTP status from %s:\n%s", name, out)
	return status
}

// MeshRejectsPlaintext proves strict mTLS and the default deny: a pod outside the mesh is refused, and so is a pod
// in the mesh that no allow policy names, after ztunnel authenticated it.
var _ = Describe("MeshRejectsPlaintext", Label("sandbox", "testbed", "mesh"), func() {
	It("refuses plaintext and unlisted meshed callers", func(ctx SpecContext) {
		// Only this run's log lines: an earlier run's rejection must not satisfy the assertions below
		since := time.Now().UTC().Format(time.RFC3339)
		target := "http://excalidraw.excalidraw"
		Expect(meshCurlIn(ctx, "default", "mesh-plain", target, "")).To(Equal("000"), "a pod outside the mesh reached a meshed service in plaintext")
		// In the mesh but not allowed into excalidraw (default deny): refused after the mTLS handshake
		Expect(meshCurlIn(ctx, "default", "mesh-member", target, "istio.io/dataplane-mode=ambient")).To(Equal("000"))

		// Every node's ztunnel (the connections may have gone through any of them); a selector alone keeps only 10 lines
		logs, err := kube.Kubectl(ctx, "istio-system", "logs", "--selector=app=ztunnel", "--prefix", "--since-time="+since, "--tail=-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(logs).To(ContainSubstring("policy rejection"), "ztunnel did not log the refused plaintext connection")
		Expect(logs).To(ContainSubstring(`src.identity="spiffe://cluster.local/ns/default/sa/default"`),
			"ztunnel did not authenticate the meshed pod (mTLS)")
		Expect(logs).To(MatchRegexp(`src.identity="spiffe://cluster.local/ns/default/sa/default"[^\n]*policy rejection|policy rejection[^\n]*src.identity="spiffe://cluster.local/ns/default/sa/default"`),
			"ztunnel did not refuse the meshed pod by policy")
	}, SpecTimeout(20*time.Minute))
})

// MeshAllowsSameNamespace proves the `allow` policies' same-namespace rule: a meshed pod inside excalidraw reaches
// excalidraw.
var _ = Describe("MeshAllowsSameNamespace", Label("sandbox", "testbed", "mesh"), func() {
	It("lets a neighbour in", func(ctx SpecContext) {
		Expect(meshCurlIn(ctx, "excalidraw", "mesh-neighbour", "http://excalidraw.excalidraw", "")).To(Equal("200"))
	}, SpecTimeout(10*time.Minute))
})

// MeshPrometheusTargetsUp proves the allows for Prometheus: no scrape target is down.
var _ = Describe("MeshPrometheusTargetsUp", Label("sandbox", "testbed", "mesh"), func() {
	It("has no scrape target down", func(ctx SpecContext) {
		Eventually(ctx, func() bool {
			metrics, err := prometheusQuery(ctx, "up == 0")
			if err != nil {
				return false
			}
			for _, m := range metrics {
				GinkgoWriter.Printf("target down: %v\n", m)
			}
			return len(metrics) == 0
		}).WithTimeout(5*time.Minute).WithPolling(30*time.Second).Should(BeTrue(), "Prometheus has targets down")
	}, SpecTimeout(10*time.Minute))
})
