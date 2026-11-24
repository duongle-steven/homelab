package e2e

import (
	"strconv"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// NodeMeshAgents (docs/superpowers/specs/2026-11-25-istio-cni-k3s-name-design.md): istio-cni in native nftables mode
// finds the kubelet's UID by a process named exactly k3s, which host-installed k3s (k3s-server) is not, so every node
// runs one (infra/_modules/metal's k3s-name-for-istio), owned by root like k3s; with it istio-cni and ztunnel run on
// every node
var _ = Describe("NodeMeshAgents", Label("testbed", "production", "proof"), func() {
	It("runs a root process named k3s on every node", func(ctx SpecContext) {
		names, ips := metalHosts()
		for _, name := range names {
			uids, err := testbedSSH(ctx, ips[name],
				`for p in /proc/[0-9]*; do [ "$(cat $p/comm 2>/dev/null)" = k3s ] && awk '/^Uid/{print $2}' $p/status; done`)
			Expect(err).NotTo(HaveOccurred(), uids)
			Expect(strings.Fields(uids)).To(ContainElement("0"), "%s: no process named k3s owned by root", name)
		}
	}, SpecTimeout(5*time.Minute))

	It("runs istio-cni and ztunnel on every node", func(ctx SpecContext) {
		names, _ := metalHosts()
		for _, ds := range []string{"istio-cni-node", "ztunnel"} {
			Eventually(ctx, func() string {
				out, _ := kube.Kubectl(ctx, "istio-system", "get", "daemonset", ds,
					"-o", "jsonpath={.status.desiredNumberScheduled} {.status.numberReady}")
				return out
			}).WithTimeout(15*time.Minute).WithPolling(15*time.Second).
				Should(Equal(strconv.Itoa(len(names))+" "+strconv.Itoa(len(names))), "%s: desired and ready on every node", ds)
		}
	}, SpecTimeout(20*time.Minute))
})
