package e2e

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Production's hardware (step 12b-1). The specs labelled hardware-proof power nodes off: a routine run against
// production (just test e2e --env production, no --labels) leaves them out, so they run only when asked by label

// wakeOnLAN sends a magic packet for mac from the workstation's own address on the nodes' /24, to that subnet's
// broadcast and to the limited broadcast. Unbound, the packets left WSL (mirrored networking) by another interface
// (Docker's bridges) and woke nothing; bound to the LAN address, as nixie binds its own, they wake the node (12b-1)
func wakeOnLAN(mac string, ips map[string]string) error {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return err
	}
	packet := make([]byte, 0, 102)
	for range 6 {
		packet = append(packet, 0xff)
	}
	for range 16 {
		packet = append(packet, hw...)
	}
	var subnet net.IP
	for _, ip := range ips {
		if v4 := net.ParseIP(ip).To4(); v4 != nil {
			subnet = v4.Mask(net.CIDRMask(24, 32))
		}
	}
	if subnet == nil {
		return fmt.Errorf("no node address to wake %s on", mac)
	}
	local, err := localAddressIn(subnet)
	if err != nil {
		return err
	}
	broadcast := net.IPv4(subnet[0], subnet[1], subnet[2], 255)
	sent := 0
	for _, target := range []net.IP{broadcast, net.IPv4bcast} {
		conn, err := net.DialUDP("udp4", &net.UDPAddr{IP: local}, &net.UDPAddr{IP: target, Port: 9})
		if err != nil {
			GinkgoWriter.Printf("Wake-on-LAN to %s from %s: %v\n", target, local, err)
			continue
		}
		if _, err := conn.Write(packet); err == nil {
			sent++
		}
		conn.Close()
	}
	if sent == 0 {
		return fmt.Errorf("no Wake-on-LAN packet for %s went out from %s", mac, local)
	}
	return nil
}

// localAddressIn is the workstation's IPv4 address on subnet (a /24), from its interfaces
func localAddressIn(subnet net.IP) (net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, addr := range addrs {
		if n, ok := addr.(*net.IPNet); ok {
			if v4 := n.IP.To4(); v4 != nil && v4.Mask(net.CIDRMask(24, 32)).Equal(subnet) {
				return v4, nil
			}
		}
	}
	return nil, fmt.Errorf("this machine has no address on %s/24: Wake-on-LAN cannot reach the nodes", subnet)
}

func bootID(ctx context.Context, node string) string {
	id, _ := kube.Kubectl(ctx, "", "get", "node", node, "-o", "jsonpath={.status.nodeInfo.bootID}")
	return id
}

// timedLookups times two lookups on the far side (a node over SSH, or a pod), so SSH's or kubectl's own time is not
// counted: /proc/uptime has 10 ms steps, enough against a 1 s budget, and busybox's date has no %N
const timedLookups = `ms() { awk '{ print int($1 * 1000) }' /proc/uptime; }
t() { s=$(ms); "$@" >/dev/null 2>&1; e=$(ms); echo $((e - s)); }
t %[1]s nx-%[2]d.example.com
t %[3]s ipv4only.arpa`

// The ISP's modem intercepts every UDP/53 query leaving the LAN and answers negative ones after 6-13 s: production's
// nodes resolve through the Archer's DoH (config.json's network.dns), and pods through CoreDNS, which follows the node
var _ = Describe("NodesResolveQuickly", Label("production"), func() {
	It("answers a missing name and a missing AAAA within a second, on the nodes and in a pod", func(ctx SpecContext) {
		check := func(where, out string, err error) {
			GinkgoHelper()
			Expect(err).NotTo(HaveOccurred(), "%s: %s", where, out)
			lines := strings.Split(strings.TrimSpace(out), "\n")
			Expect(lines).To(HaveLen(2), "%s: %s", where, out)
			for i, what := range []string{"an NXDOMAIN", "an AAAA-less name"} {
				ms, err := strconv.Atoi(strings.TrimSpace(lines[i]))
				Expect(err).NotTo(HaveOccurred(), "%s: %s", where, out)
				GinkgoWriter.Printf("%s: %s in %d ms\n", where, what, ms)
				Expect(ms).To(BeNumerically("<", 1000), "%s: %s took %d ms (the modem's interception?)", where, what, ms)
			}
		}
		names, ips := metalHosts()
		for _, name := range names {
			script := fmt.Sprintf(timedLookups, "getent hosts", time.Now().UnixNano(), "getent ahostsv6")
			out, err := testbedSSH(ctx, ips[name], script)
			check(name, out, err)
		}

		namespace := fmt.Sprintf("dns-test-%d", time.Now().Unix())
		Expect(kube.CreateNamespace(ctx, namespace)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { _ = kube.DeleteNamespace(ctx, namespace) }, NodeTimeout(5*time.Minute))
		script := fmt.Sprintf(timedLookups, "nslookup", time.Now().UnixNano(), "nslookup -type=AAAA")
		Expect(kube.Apply(ctx, namespace, fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"dns"},"spec":{
			"restartPolicy":"Never","containers":[{"name":"c","image":"docker.io/library/busybox:1.37.0","command":["sh","-c",%q]}]}}`,
			script))).To(Succeed())
		waitPodSucceeded(ctx, namespace, "dns")
		out, err := kube.Kubectl(ctx, namespace, "logs", "dns")
		check("a pod", out, err)
	}, SpecTimeout(10*time.Minute))
})

// The API's VIP moves off a node that powers off, and Wake-on-LAN from the workstation brings the node back: it boots to
// the network (BIOS: Wake-on-LAN = Boot to Network), finds no nixie and falls back to its NVMe
var _ = Describe("ProductionVIPMoves", Label("production", "hardware-proof"), Serial, func() {
	It("moves the API's VIP off a powered-off node, and wakes the node by Wake-on-LAN", func(ctx SpecContext) {
		var holder string
		Eventually(ctx, func() string {
			holder, _ = kube.Kubectl(ctx, "kube-system", "get", "lease", "plndr-cp-lock", "-o", "jsonpath={.spec.holderIdentity}")
			return holder
		}).WithTimeout(5*time.Minute).WithPolling(10*time.Second).ShouldNot(BeEmpty(), "no kube-vip leader for the control plane")
		_, ips := metalHosts()
		macs := metalMACs()
		Expect(ips).To(HaveKey(holder))
		booted := bootID(ctx, holder)
		GinkgoWriter.Printf("%s holds the VIP: powering it off\n", holder)
		// The session ends with the machine: its error is expected
		_, _ = testbedSSH(ctx, ips[holder], "systemctl poweroff")
		DeferCleanup(func(ctx SpecContext) {
			Expect(wakeOnLAN(macs[holder], ips)).To(Succeed())
			start := time.Now()
			Eventually(ctx, func() bool { id := bootID(ctx, holder); return id != "" && id != booted }).
				WithTimeout(10*time.Minute).WithPolling(10*time.Second).
				Should(BeTrue(), "%s did not come back after Wake-on-LAN: press its power button, then check its BIOS (Wake-on-LAN = Boot to Network, network boot on)", holder)
			waitNodeReady(ctx, holder, 10*time.Minute)
			GinkgoWriter.Printf("%s came back by Wake-on-LAN in %s\n", holder, time.Since(start).Round(time.Second))
		}, NodeTimeout(25*time.Minute))
		start := time.Now()
		Eventually(ctx, func() string {
			next, _ := kube.Kubectl(ctx, "kube-system", "get", "lease", "plndr-cp-lock", "-o", "jsonpath={.spec.holderIdentity}")
			return next
		}).WithTimeout(3*time.Minute).WithPolling(5*time.Second).ShouldNot(Or(Equal(holder), BeEmpty()), "the VIP did not move off %s", holder)
		Eventually(ctx, func() string {
			out, _ := kube.Kubectl(ctx, "", "get", "--raw", "/readyz")
			return out
		}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(Equal("ok"), "the API did not answer through the VIP without %s", holder)
		GinkgoWriter.Printf("the API answered through the VIP %s after %s powered off\n", time.Since(start).Round(time.Second), holder)
	}, SpecTimeout(35*time.Minute))
})

// Every node loses power at once, by a person at the power strip: each boots by itself (BIOS: After Power Loss =
// Previous State), and etcd and the API at its VIP come back. Data written before the cut is checked in 12b-2, with Ceph
var _ = Describe("ProductionPowerCut", Label("production", "hardware-proof"), Serial, func() {
	It("comes back by itself after every node loses power", func(ctx SpecContext) {
		names, _ := metalHosts()
		booted := map[string]string{}
		for _, name := range names {
			waitNodeReady(ctx, name, 5*time.Minute)
			booted[name] = bootID(ctx, name)
		}
		GinkgoWriter.Println("NOW: switch the power strip off, wait 30 seconds, switch it on")
		Eventually(ctx, func() error {
			_, err := kube.Kubectl(ctx, "", "get", "--raw", "/readyz")
			return err
		}).WithTimeout(15*time.Minute).WithPolling(2*time.Second).Should(HaveOccurred(), "the API never went away: was the power cut?")
		cut := time.Now()
		for _, name := range names {
			Eventually(ctx, func() bool { id := bootID(ctx, name); return id != "" && id != booted[name] }).
				WithTimeout(30*time.Minute).WithPolling(10*time.Second).
				Should(BeTrue(), "%s did not boot again by itself: check its BIOS (After Power Loss = Previous State)", name)
			waitNodeReady(ctx, name, 15*time.Minute)
		}
		Eventually(ctx, func() string {
			out, _ := kube.Kubectl(ctx, "", "get", "--raw", "/readyz")
			return out
		}).WithTimeout(5 * time.Minute).WithPolling(5 * time.Second).Should(Equal("ok"))
		GinkgoWriter.Printf("every node Ready and the API at its VIP %s after the power went\n", time.Since(cut).Round(time.Second))
	}, SpecTimeout(60*time.Minute))
})
