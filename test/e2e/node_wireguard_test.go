package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/duongle-steven/homelab/test/internal/kube"
)

// G12 (docs/superpowers/specs/2026-11-26-lan-encryption-design.md): every node carries pod traffic through flannel's
// WireGuard, with a peer for each other node's current key (its Node annotation) routing that node's pod subnet (a node
// whose key the others have not learned yet carries nothing to them), no VXLAN device left, and pods' MTU at
// WireGuard's 1420. flannel keeps its key in /run, so a node makes a new one at every boot, and a node that started
// before it keeps a peer for the old key with no allowed IPs: it routes nothing and nobody holds its private key any
// more, so only peers that route something are counted.
var _ = Describe("NodeWireGuard", Label("testbed", "production", "proof"), Ordered, ContinueOnFailure, func() {
	var names []string
	var ips map[string]string
	BeforeAll(func() { names, ips = metalHosts() })

	It("carries pod traffic through WireGuard on every node", func(ctx SpecContext) {
		for _, name := range names {
			Eventually(ctx, func() (map[string]string, error) {
				routed := map[string]string{}
				out, err := testbedSSH(ctx, ips[name], "wg show flannel-wg allowed-ips")
				if err != nil {
					return nil, err
				}
				for _, line := range strings.Split(out, "\n") {
					if f := strings.Fields(line); len(f) > 1 && f[1] != "(none)" {
						routed[f[0]] = strings.Join(f[1:], " ")
					}
				}
				return routed, nil
			}).WithTimeout(2*time.Minute).WithPolling(10*time.Second).Should(Equal(otherNodesWireGuard(ctx, name)),
				"%s: WireGuard peers routing a pod subnet, by public key", name)
			_, err := testbedSSH(ctx, ips[name], "ip link show flannel.1")
			Expect(err).To(HaveOccurred(), "%s: a VXLAN device is left", name)
			link, err := testbedSSH(ctx, ips[name], "ip -o link show flannel-wg")
			Expect(err).NotTo(HaveOccurred(), link)
			Expect(link).To(ContainSubstring("mtu 1420"), name)
		}
	}, SpecTimeout(10*time.Minute))
})

// otherNodesWireGuard maps every other node's current WireGuard public key to its pod subnet
func otherNodesWireGuard(ctx context.Context, self string) map[string]string {
	GinkgoHelper()
	out, err := kube.Kubectl(ctx, "", "get", "nodes", "-o",
		`jsonpath={range .items[*]}{.metadata.name}{"\t"}{.spec.podCIDR}{"\t"}{.metadata.annotations.flannel\.alpha\.coreos\.com/backend-data}{"\n"}{end}`)
	Expect(err).NotTo(HaveOccurred(), out)
	want := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 || f[0] == self {
			continue
		}
		var data struct{ PublicKey string }
		Expect(json.Unmarshal([]byte(f[2]), &data)).To(Succeed(), "%s: flannel's backend-data", f[0])
		want[data.PublicKey] = f[1]
	}
	return want
}
