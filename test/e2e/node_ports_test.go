package e2e

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The namespace of NodePorts' throwaway pods
const nodePortsNamespace = "node-ports-test"

// NodePorts (G2, docs/superpowers/specs/2026-11-24-node-ports-design.md), from the workstation, which on the testbed
// sits on the VMs' bridge like any LAN device and on production is one: the cluster's own ports answer only the nodes
// (the kubelet also the pods), SSH signs in by key only, the uplink has no IPv6, and the cluster still works through
// every port that closed
var _ = Describe("NodePorts", Label("testbed", "production", "proof"), Ordered, ContinueOnFailure, func() {
	var names []string
	var ips map[string]string
	BeforeAll(func() { names, ips = metalHosts() })

	It("lets the LAN reach only what stays open", func(ctx SpecContext) {
		for _, name := range names {
			// The control: the workstation reaches the node at all
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(ips[name], "6443"), 5*time.Second)
			Expect(err).NotTo(HaveOccurred(), "%s: 6443", name)
			conn.Close()
			for _, port := range []string{"2379", "2380", "10250"} {
				conn, err := net.DialTimeout("tcp", net.JoinHostPort(ips[name], port), 5*time.Second)
				if err == nil {
					conn.Close()
				}
				Expect(err).To(HaveOccurred(), "%s: %s answers the LAN", name, port)
			}
		}
	}, SpecTimeout(5*time.Minute))

	// A VXLAN drop cannot be seen from outside: the node's rules say it
	It("accepts VXLAN only from the nodes", func(ctx SpecContext) {
		for _, name := range names {
			rules, err := testbedSSH(ctx, ips[name], "iptables -S nixos-fw")
			Expect(err).NotTo(HaveOccurred(), rules)
			var vxlan []string
			for _, line := range strings.Split(rules, "\n") {
				if strings.Contains(line, "8472") {
					vxlan = append(vxlan, line)
				}
			}
			Expect(vxlan).To(HaveLen(len(names)), "%s: one VXLAN rule per node: %v", name, vxlan)
			for _, node := range names {
				Expect(vxlan).To(ContainElement(ContainSubstring("-s "+ips[node]+"/32 ")), "%s: VXLAN from %s", name, node)
			}
		}
	}, SpecTimeout(5*time.Minute))

	It("has no IPv6 on the uplink", func(ctx SpecContext) {
		for _, name := range names {
			global, err := testbedSSH(ctx, ips[name], "ip -o -6 addr show scope global")
			Expect(err).NotTo(HaveOccurred(), global)
			Expect(global).To(BeEmpty(), "%s: a global IPv6 address", name)
			// The uplink is the interface holding the node's IPv4 address
			uplink, err := testbedSSH(ctx, ips[name], fmt.Sprintf(`ip -o -4 addr show | awk -v ip=%s '$4 ~ "^"ip"/" {print $2}'`, ips[name]))
			Expect(err).NotTo(HaveOccurred(), uplink)
			Expect(uplink).NotTo(BeEmpty(), name)
			v6, err := testbedSSH(ctx, ips[name], "ip -o -6 addr show dev "+uplink)
			Expect(err).NotTo(HaveOccurred(), v6)
			Expect(v6).To(BeEmpty(), "%s: IPv6 on %s", name, uplink)
			// The mechanism, not only today's state: the kernel ignores router advertisements (networkd handles them, and
			// is told to ignore them too) and makes no link-local address, so a later advertisement gives the link
			// nothing; DHCPv6 is off. IPv6 itself stays on (disable_ipv6 0): loopback keeps ::1
			for key, want := range map[string]string{"accept_ra": "0", "addr_gen_mode": "1"} {
				got, err := testbedSSH(ctx, ips[name], "cat /proc/sys/net/ipv6/conf/"+uplink+"/"+key)
				Expect(err).NotTo(HaveOccurred(), got)
				Expect(strings.TrimSpace(got)).To(Equal(want), "%s: %s on %s", name, key, uplink)
			}
		}
	}, SpecTimeout(5*time.Minute))

	It("signs in to SSH by key only", func(ctx SpecContext) {
		for _, name := range names {
			config, err := testbedSSH(ctx, ips[name], "sshd -T")
			Expect(err).NotTo(HaveOccurred(), config)
			// sshd -T prints lower-case keys on older OpenSSH and CamelCase ones on 10.x, which also names root's key-only
			// login prohibit-password where older releases said without-password
			lines := strings.Split(strings.ToLower(strings.ReplaceAll(config, "without-password", "prohibit-password")), "\n")
			Expect(lines).To(ContainElements("passwordauthentication no", "kbdinteractiveauthentication no", "permitrootlogin prohibit-password"), name)
		}
	}, SpecTimeout(5*time.Minute))

	It("keeps every etcd member healthy", func(ctx SpecContext) {
		tls := "/var/lib/rancher/k3s/server/tls/etcd"
		Eventually(ctx, func() int {
			out, _ := testbedSSH(ctx, ips[names[0]], fmt.Sprintf("etcdctl --endpoints https://127.0.0.1:2379 --cacert %[1]s/server-ca.crt --cert %[1]s/server-client.crt --key %[1]s/server-client.key endpoint health --cluster 2>&1", tls))
			return strings.Count(out, "is healthy")
		}).WithTimeout(2*time.Minute).WithPolling(10*time.Second).Should(Equal(len(names)), "etcd's members through 2379 and 2380")
	}, SpecTimeout(5*time.Minute))

	// The API server reaches every kubelet (logs, exec), and a pod reaches its own node's kubelet and another's
	// (Prometheus's scrape): flannel masquerades the second to its node's address, so only the first goes through the
	// pod network's rule (the G2 review)
	It("lets the API server and the pods reach every kubelet", func(ctx SpecContext) {
		Expect(kube.CreateNamespace(ctx, nodePortsNamespace)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { _ = kube.DeleteNamespace(ctx, nodePortsNamespace) }, NodeTimeout(2*time.Minute))
		for _, name := range names {
			Expect(kube.Apply(ctx, nodePortsNamespace, fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata: {name: kubelet-%[1]s}
spec:
  nodeName: %[1]s
  restartPolicy: Never
  securityContext: {runAsNonRoot: true, runAsUser: 65532, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: curl
      image: docker.io/curlimages/curl:8.23.0
      command: [sh, -c, 'echo ready; sleep 3600']
      securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}}
`, name))).To(Succeed())
		}
		for i, name := range names {
			pod := "kubelet-" + name
			out, err := kube.Kubectl(ctx, nodePortsNamespace, "wait", "--for=condition=Ready", "pod/"+pod, "--timeout=5m")
			Expect(err).NotTo(HaveOccurred(), out)
			Eventually(ctx, func() string {
				logs, _ := kube.Kubectl(ctx, nodePortsNamespace, "logs", pod)
				return logs
			}).WithTimeout(time.Minute).Should(ContainSubstring("ready"), "kubectl logs on %s", name)
			for _, kubelet := range []string{ips[name], ips[names[(i+1)%len(names)]]} {
				code, err := kube.Kubectl(ctx, nodePortsNamespace, "exec", pod, "--", "curl", "-sk", "--max-time", "10", "-o", "/dev/null",
					"-w", "%{http_code}", "https://"+kubelet+":10250/healthz")
				Expect(err).NotTo(HaveOccurred(), "kubectl exec on %s, or the kubelet at %s: %s", name, kubelet, code)
				Expect(strings.TrimSpace(code)).To(BeElementOf("401", "403"), "%s's pod to the kubelet at %s", name, kubelet)
			}
		}
	}, SpecTimeout(15*time.Minute))
})
