package e2e

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// G12 (docs/superpowers/specs/2026-11-26-lan-encryption-design.md): every node carries pod traffic through flannel's
// WireGuard, with a peer for each other node (a node whose key the others have not learned yet carries nothing to
// them), no VXLAN device left, and pods' MTU at WireGuard's 1420
var _ = Describe("NodeWireGuard", Label("testbed", "production", "proof"), Ordered, ContinueOnFailure, func() {
	var names []string
	var ips map[string]string
	BeforeAll(func() { names, ips = metalHosts() })

	It("carries pod traffic through WireGuard on every node", func(ctx SpecContext) {
		for _, name := range names {
			Eventually(ctx, func() int {
				peers, err := testbedSSH(ctx, ips[name], "wg show flannel-wg peers")
				if err != nil {
					return -1
				}
				return len(strings.Fields(peers))
			}).WithTimeout(2*time.Minute).WithPolling(10*time.Second).Should(Equal(len(names)-1), "%s: WireGuard peers", name)
			_, err := testbedSSH(ctx, ips[name], "ip link show flannel.1")
			Expect(err).To(HaveOccurred(), "%s: a VXLAN device is left", name)
			link, err := testbedSSH(ctx, ips[name], "ip -o link show flannel-wg")
			Expect(err).NotTo(HaveOccurred(), link)
			Expect(link).To(ContainSubstring("mtu 1420"), name)
		}
	}, SpecTimeout(10*time.Minute))
})
