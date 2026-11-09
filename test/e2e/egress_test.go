package e2e

import (
	"context"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// egressCurl probes from a new pod after the network policy controller has had time to apply the namespace's
// policies to it (it does so asynchronously: a pod's first seconds are not yet restricted)
func egressCurl(ctx context.Context, namespace, target string) string {
	return curlFrom(ctx, namespace, "egress-probe", target, "", 20)
}

// egressBlocked probes a target that the policies must block, up to three times: right after the cluster starts (a
// restarted sandbox), while every pod restarts, the controller can take longer than the probe's wait to restrict a
// new pod. A real hole never answers 000
func egressBlocked(ctx context.Context, namespace, target string) string {
	var status string
	for range 3 {
		if status = egressCurl(ctx, namespace, target); status == "000" {
			break
		}
		GinkgoWriter.Printf("%s reached %s (%s): probing again in case its policies were not applied yet\n", namespace, target, status)
	}
	return status
}

// waitForEgressPolicies waits until Argo CD has synced system/egress into a namespace: these specs run before the
// slower ones, while a fresh sandbox is still syncing
func waitForEgressPolicies(ctx context.Context, namespace string) {
	Eventually(ctx, func() error {
		_, err := kube.Kubectl(ctx, namespace, "get", "networkpolicy", "deny-all-egress")
		return err
	}).WithTimeout(30*time.Minute).WithPolling(10*time.Second).Should(Succeed(), "no egress policies in %s", namespace)
}

// A namespace not in system/egress's internet table reaches neither the internet nor the nodes' other ports. The
// internet probe uses an address, not a name, so a DNS failure cannot pass for a block
var _ = Describe("EgressBlocksInternet", Label("sandbox", "testbed", "egress"), func() {
	It("blocks excalidraw from the internet", func(ctx SpecContext) {
		waitForEgressPolicies(ctx, "excalidraw")
		Expect(egressBlocked(ctx, "excalidraw", "https://1.1.1.1")).To(Equal("000"), "excalidraw reached the internet")
	}, SpecTimeout(50*time.Minute))
})

var _ = Describe("EgressBlocksNodePorts", Label("sandbox", "testbed", "egress"), func() {
	It("blocks the kubelet, not the API server", func(ctx SpecContext) {
		waitForEgressPolicies(ctx, "excalidraw")
		node := gatewayAddress()
		Expect(egressBlocked(ctx, "excalidraw", "https://"+node+":10250/")).To(Equal("000"), "excalidraw reached the kubelet")
		Expect(egressCurl(ctx, "excalidraw", "https://kubernetes.default.svc/version")).NotTo(Equal("000"), "excalidraw lost the API server")
	}, SpecTimeout(55*time.Minute))
})

// A namespace in the table reaches the internet on its ports
var _ = Describe("EgressAllowsListedNamespaces", Label("sandbox", "testbed", "egress"), func() {
	It("lets argocd reach the internet", func(ctx SpecContext) {
		waitForEgressPolicies(ctx, "argocd")
		Expect(egressCurl(ctx, "argocd", "https://github.com")).NotTo(Equal("000"), "argocd lost the internet")
	}, SpecTimeout(40*time.Minute))
})
