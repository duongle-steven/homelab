package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/clusterscan"
	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/repo"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The testbed's specs (label testbed) run against its three NixOS VMs (just testbed: ENV=testbed)

// testbedHosts reads the VMs' names and addresses (infra/testbed/metal/hosts.json), sorted by name
func testbedHosts() (names []string, ips map[string]string) {
	GinkgoHelper()
	raw, err := os.ReadFile(filepath.Join(repo.Root(), "infra", "testbed", "metal", "hosts.json"))
	Expect(err).NotTo(HaveOccurred())
	var hosts map[string]struct {
		IP string `json:"ip"`
	}
	Expect(json.Unmarshal(raw, &hosts)).To(Succeed())
	ips = map[string]string{}
	for name, host := range hosts {
		names = append(names, name)
		ips[name] = host.IP
	}
	sort.Strings(names)
	return names, ips
}

func testbedSSH(ctx context.Context, ip, command string) (string, error) {
	out, err := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "root@"+ip, command).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func virsh(ctx context.Context, args ...string) string {
	GinkgoHelper()
	out, err := exec.CommandContext(ctx, "virsh", append([]string{"-c", "qemu:///system"}, args...)...).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
	return strings.TrimSpace(string(out))
}

// domstate reports a VM's state without failing the spec: it runs inside Eventually's condition
func domstate(ctx context.Context, name string) string {
	out, _ := exec.CommandContext(ctx, "virsh", "-c", "qemu:///system", "domstate", name).Output()
	return strings.TrimSpace(string(out))
}

// waitNodeReady waits until node reports Ready
func waitNodeReady(ctx context.Context, node string, wait time.Duration) {
	GinkgoHelper()
	Eventually(ctx, func() string {
		out, _ := kube.Kubectl(ctx, "", "get", "node", node, "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
		return out
	}).WithTimeout(wait).WithPolling(10*time.Second).Should(Equal("True"), "%s is not Ready", node)
}

// testbedList is the root ApplicationSet's directory list for the testbed
func testbedList(ctx context.Context) []string {
	GinkgoHelper()
	listed, err := exec.CommandContext(ctx, "yq", `.argocd-apps.applicationsets.root.generators[0].matrix.generators[0].git.directories[].path`,
		filepath.Join(repo.Root(), "system/argocd/values-testbed.yaml")).Output()
	Expect(err).NotTo(HaveOccurred())
	return strings.Fields(string(listed))
}

// Before the other specs read the running result: every Application the list names has synced this commit and is
// healthy. Without this a change Argo CD has not reached yet, or a sync that failed, would be marked as proved
var _ = Describe("TestbedSynced", Label("testbed", "proof"), func() {
	It("synced this commit", func(ctx SpecContext) {
		head, err := exec.CommandContext(ctx, "git", "-C", repo.Root(), "rev-parse", "HEAD").Output()
		Expect(err).NotTo(HaveOccurred())
		commit := strings.TrimSpace(string(head))
		var want []string
		for _, path := range testbedList(ctx) {
			want = append(want, filepath.Base(path))
		}
		Expect(want).NotTo(BeEmpty())
		var waiting []string
		Eventually(ctx, func() bool {
			waiting = nil
			for _, name := range want {
				var app struct {
					Status struct {
						Sync struct {
							Status    string   `json:"status"`
							Revision  string   `json:"revision"`
							Revisions []string `json:"revisions"`
						} `json:"sync"`
						Health struct {
							Status string `json:"status"`
						} `json:"health"`
					} `json:"status"`
				}
				if kube.GetJSON(ctx, "argocd", &app, "application", name) != nil {
					waiting = append(waiting, name+": not read")
					continue
				}
				revisions := append(app.Status.Sync.Revisions, app.Status.Sync.Revision)
				atCommit := false
				for _, revision := range revisions {
					atCommit = atCommit || revision == commit
				}
				if app.Status.Sync.Status != "Synced" || app.Status.Health.Status != "Healthy" || !atCommit {
					waiting = append(waiting, fmt.Sprintf("%s: %s %s %v", name, app.Status.Sync.Status, app.Status.Health.Status, revisions))
				}
			}
			return len(waiting) == 0
		}).WithTimeout(35*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), func() string {
			return fmt.Sprintf("still waiting: %v", waiting)
		})
	}, SpecTimeout(40*time.Minute))
})

// TestbedSecretsEncrypted reads a Secret raw from etcd on a node (TestSecretIsEncrypted proves the check can fail).
// Only etcdctl's stdout: ssh's warnings would hide the prefix
var _ = Describe("TestbedSecretsEncrypted", Label("testbed", "proof"), func() {
	It("encrypts Secrets in etcd", func(ctx SpecContext) {
		names, ips := testbedHosts()
		random := make([]byte, 16)
		_, err := rand.Read(random)
		Expect(err).NotTo(HaveOccurred())
		value, name := hex.EncodeToString(random), "secrets-encrypted-test"
		DeferCleanup(func(ctx SpecContext) {
			_, _ = kube.Kubectl(ctx, "kube-system", "delete", "secret", name, "--ignore-not-found")
		}, NodeTimeout(time.Minute))
		Expect(kube.Apply(ctx, "kube-system", fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata: {name: %s}
stringData: {value: %s}
`, name, value))).To(Succeed())

		tls := "/var/lib/rancher/k3s/server/tls/etcd"
		raw, err := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "root@"+ips[names[0]],
			fmt.Sprintf("etcdctl --endpoints https://127.0.0.1:2379 --cacert %[1]s/server-ca.crt --cert %[1]s/server-client.crt --key %[1]s/server-client.key get /registry/secrets/kube-system/%[2]s --print-value-only", tls, name)).Output()
		Expect(err).NotTo(HaveOccurred())
		Expect(clusterscan.SecretIsEncrypted(raw, value)).To(Succeed())
	}, SpecTimeout(10*time.Minute))
})

// Three k3s servers, each an etcd member that sees a leader
var _ = Describe("TestbedNodes", Label("testbed", "proof"), func() {
	It("runs three etcd members with a leader", func(ctx SpecContext) {
		names, ips := testbedHosts()
		Expect(names).To(HaveLen(3))
		for _, name := range names {
			waitNodeReady(ctx, name, 5*time.Minute)
			roles, err := kube.Kubectl(ctx, "", "get", "node", name, "-o", "jsonpath={.metadata.labels}")
			Expect(err).NotTo(HaveOccurred())
			Expect(roles).To(ContainSubstring(`"node-role.kubernetes.io/control-plane":"true"`), name)
			Expect(roles).To(ContainSubstring(`"node-role.kubernetes.io/etcd":"true"`), name)
			metrics, err := testbedSSH(ctx, ips[name], "curl -s http://127.0.0.1:2381/metrics | grep '^etcd_server_has_leader '")
			Expect(err).NotTo(HaveOccurred(), metrics)
			Expect(metrics).To(Equal("etcd_server_has_leader 1"), name)
		}
	}, SpecTimeout(20*time.Minute))
})

// The API's VIP moves: with the node holding it shut down, the API still answers through the VIP from another node
var _ = Describe("TestbedVIPMoves", Label("testbed", "proof"), func() {
	It("moves the API's VIP off a shut-down node", func(ctx SpecContext) {
		holder, err := kube.Kubectl(ctx, "kube-system", "get", "lease", "plndr-cp-lock", "-o", "jsonpath={.spec.holderIdentity}")
		Expect(err).NotTo(HaveOccurred())
		Expect(holder).NotTo(BeEmpty(), "no kube-vip leader for the control plane")
		bootID := func(ctx context.Context) string {
			id, _ := kube.Kubectl(ctx, "", "get", "node", holder, "-o", "jsonpath={.status.nodeInfo.bootID}")
			return id
		}
		booted := bootID(ctx)
		GinkgoWriter.Printf("%s holds the VIP: shutting it down\n", holder)
		virsh(ctx, "shutdown", holder)
		DeferCleanup(func(ctx SpecContext) {
			Eventually(ctx, func() string { return domstate(ctx, holder) }).WithTimeout(3 * time.Minute).WithPolling(5 * time.Second).Should(Equal("shut off"))
			virsh(ctx, "start", holder)
			// The API server reports the node's last status until its kubelet reports again: wait for the new boot
			Eventually(ctx, func() bool { id := bootID(ctx); return id != "" && id != booted }).
				WithTimeout(10*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "%s did not come back", holder)
			waitNodeReady(ctx, holder, 10*time.Minute)
		}, NodeTimeout(25*time.Minute))
		Eventually(ctx, func() string { return domstate(ctx, holder) }).WithTimeout(3*time.Minute).WithPolling(5*time.Second).
			Should(Equal("shut off"), "%s did not shut down", holder)
		start := time.Now()
		Eventually(ctx, func() string {
			out, _ := kube.Kubectl(ctx, "", "get", "--raw", "/readyz")
			return out
		}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(Equal("ok"), "the API did not answer through the VIP without %s", holder)
		GinkgoWriter.Printf("the API answered through the VIP %s after %s's shutdown\n", time.Since(start).Round(time.Second), holder)
		next, err := kube.Kubectl(ctx, "kube-system", "get", "lease", "plndr-cp-lock", "-o", "jsonpath={.spec.holderIdentity}")
		Expect(err).NotTo(HaveOccurred())
		Expect(next).NotTo(Equal(holder), "the VIP did not move")
	}, SpecTimeout(15*time.Minute))
})

// Ceph on three nodes: healthy, an OSD on each node, and a volume written on one node read back from another
var _ = Describe("TestbedCeph", Label("testbed", "proof"), func() {
	It("serves a volume across nodes", func(ctx SpecContext) {
		Eventually(ctx, func() string {
			health, _ := kube.Kubectl(ctx, "rook-ceph", "get", "cephcluster", "-o", "jsonpath={.items[*].status.ceph.health}")
			return health
		}).WithTimeout(30*time.Minute).WithPolling(15*time.Second).Should(Equal("HEALTH_OK"), "Ceph is not HEALTH_OK")
		names, _ := testbedHosts()
		// HEALTH_OK can come while Rook still recreates an OSD's pod
		var osds string
		Eventually(ctx, func() bool {
			osds, _ = kube.Kubectl(ctx, "rook-ceph", "get", "pods", "-l", "app=rook-ceph-osd",
				"-o", `jsonpath={range .items[*]}{.spec.nodeName}{" "}{.status.phase}{"\n"}{end}`)
			for _, name := range names {
				if !strings.Contains(osds, name+" Running") {
					return false
				}
			}
			return true
		}).WithTimeout(10*time.Minute).WithPolling(15*time.Second).Should(BeTrue(), func() string {
			return "an OSD is not running:\n" + osds
		})

		ns := fmt.Sprintf("testbed-ceph-%d", time.Now().Unix())
		Expect(kube.CreateNamespace(ctx, ns)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { Expect(kube.DeleteNamespace(ctx, ns)).To(Succeed()) }, NodeTimeout(5*time.Minute))
		pod := func(name, node, command string) string {
			return fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":%q},"spec":{"nodeName":%q,"restartPolicy":"Never",
			"containers":[{"name":"c","image":"busybox:1.37.0","command":["sh","-c",%q],"volumeMounts":[{"name":"v","mountPath":"/data"}]}],
			"volumes":[{"name":"v","persistentVolumeClaim":{"claimName":"data"}}]}}`, name, node, command)
		}
		claim := `{"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"name":"data"},
		"spec":{"accessModes":["ReadWriteOnce"],"storageClassName":"standard-rwo","resources":{"requests":{"storage":"1Gi"}}}}`
		Expect(kube.Apply(ctx, ns, claim)).To(Succeed())
		Expect(kube.Apply(ctx, ns, pod("writer", names[0], "echo testbed-ceph > /data/probe && sync"))).To(Succeed())
		waitPodSucceeded(ctx, ns, "writer")
		out, err := kube.Kubectl(ctx, ns, "delete", "pod", "writer", "--wait")
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(kube.Apply(ctx, ns, pod("reader", names[1], "cat /data/probe"))).To(Succeed())
		waitPodSucceeded(ctx, ns, "reader")
		logs, err := kube.Kubectl(ctx, ns, "logs", "reader")
		Expect(err).NotTo(HaveOccurred())
		Expect(logs).To(Equal("testbed-ceph"), "written on %s, read on %s", names[0], names[1])
	}, SpecTimeout(55*time.Minute))
})

func waitPodSucceeded(ctx context.Context, namespace, name string) {
	GinkgoHelper()
	var phase string
	Eventually(ctx, func() bool {
		phase, _ = kube.Kubectl(ctx, namespace, "get", "pod", name, "-o", "jsonpath={.status.phase}")
		return phase == "Succeeded" || phase == "Failed"
	}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "pod %s did not finish", name)
	Expect(phase).To(Equal("Succeeded"), "pod %s failed", name)
}

// A Gateway pod restarting changes its EndpointSlice: k3s's ServiceLB, which ignores loadBalancerClass, then wrote the
// Service's status empty. When that came after kube-vip's own write, nothing wrote it again and kube-proxy had no rule
// for the VIP, for good. The two race, so the spec reads every second: the empty status itself is the failure,
// whichever came last
var _ = Describe("TestbedGatewayKeepsItsAddress", Label("testbed", "proof"), func() {
	It("keeps the Gateway's address while its pod restarts", func(ctx SpecContext) {
		Eventually(ctx, func() bool { return gatewayAnswers(ctx) }).WithTimeout(30*time.Minute).WithPolling(10*time.Second).
			Should(BeTrue(), "the Gateway's VIP does not answer")
		pods, err := kube.Kubectl(ctx, "istio-system", "get", "pods", "-l", "gateway.networking.k8s.io/gateway-name=gateway", "-o", "name")
		Expect(err).NotTo(HaveOccurred())
		out, err := kube.Kubectl(ctx, "istio-system", "delete", strings.Fields(pods)[0], "--wait=false")
		Expect(err).NotTo(HaveOccurred(), out)
		// Long enough for the pod to return and for what its endpoints set off
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			status, err := kube.Kubectl(ctx, "istio-system", "get", "service", "gateway-istio", "-o", "jsonpath={.status.loadBalancer.ingress[*].ip}")
			// The API may be busy: only an answer counts
			if err == nil {
				Expect(status).NotTo(BeEmpty(), "the Service's address went away with its pod")
			}
			sleep(ctx, time.Second)
		}
		Eventually(ctx, func() bool {
			out, err := kube.Kubectl(ctx, "istio-system", "get", "deployment", "gateway-istio", "-o", "jsonpath={.status.readyReplicas}")
			return err == nil && out != "" && out != "0"
		}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "the Gateway's pod did not return")
		Eventually(ctx, func() bool { return gatewayAnswers(ctx) }).WithTimeout(2*time.Minute).WithPolling(5*time.Second).
			Should(BeTrue(), "the Gateway's VIP stopped answering")
	}, SpecTimeout(45*time.Minute))
})

// Layer 2: pods on two nodes reach each other through ztunnel, the mesh refuses what no policy allows, and the Gateway
// answers at its VIP with a certificate from the local CA
var _ = Describe("TestbedMesh", Label("testbed", "proof"), func() {
	It("carries the mesh across nodes", func(ctx SpecContext) {
		testbedMeshWorks(ctx)
	}, SpecTimeout(60*time.Minute))
})

func testbedMeshWorks(ctx context.Context) {
	GinkgoHelper()
	// Not its status: Programmed stays False after its only pod's node goes, until istiod restarts, though it serves again
	Eventually(ctx, func() bool { return gatewayAnswers(ctx) }).WithTimeout(30*time.Minute).WithPolling(10*time.Second).
		Should(BeTrue(), "the Gateway does not answer")

	names, _ := testbedHosts()
	suffix := time.Now().Unix()
	meshed := fmt.Sprintf("testbed-mesh-%d", suffix)
	stranger := fmt.Sprintf("testbed-stranger-%d", suffix)
	outside := fmt.Sprintf("testbed-outside-%d", suffix)
	for _, ns := range []struct{ name, mode string }{{meshed, "ambient"}, {stranger, "ambient"}, {outside, "none"}} {
		Expect(kube.Apply(ctx, "", fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q,"labels":{"istio.io/dataplane-mode":%q}}}`, ns.name, ns.mode))).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { Expect(kube.DeleteNamespace(ctx, ns.name)).To(Succeed()) }, NodeTimeout(5*time.Minute))
	}
	Expect(kube.Apply(ctx, meshed, fmt.Sprintf(`{"apiVersion":"security.istio.io/v1","kind":"AuthorizationPolicy","metadata":{"name":"allow","namespace":%q},
		"spec":{"action":"ALLOW","rules":[{"from":[{"source":{"namespaces":[%q]}}]}]}}`, meshed, meshed))).To(Succeed())
	Expect(kube.Apply(ctx, meshed, fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"server"},"spec":{"nodeName":%q,
		"containers":[{"name":"c","image":"busybox:1.37.0","command":["sh","-c","mkdir -p /www && echo ok > /www/index.html && exec httpd -f -p 8080 -h /www"]}]}}`, names[0]))).To(Succeed())
	// As terratest's WaitUntilPodAvailable: 40 tries 15 s apart
	Eventually(ctx, func() string {
		out, _ := kube.Kubectl(ctx, meshed, "get", "pod", "server", "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
		return out
	}).WithTimeout(10*time.Minute).WithPolling(15*time.Second).Should(Equal("True"), "the server pod is not ready")
	redirection, err := kube.Kubectl(ctx, meshed, "get", "pod", "server", "-o", `jsonpath={.metadata.annotations.ambient\.istio\.io/redirection}`)
	Expect(err).NotTo(HaveOccurred())
	Expect(redirection).To(Equal("enabled"), "the server is not redirected to ztunnel")
	ip, err := kube.Kubectl(ctx, meshed, "get", "pod", "server", "-o", "jsonpath={.status.podIP}")
	Expect(err).NotTo(HaveOccurred())

	// From another node, so the connection crosses between two ztunnels
	curl := func(namespace, name string) string {
		Expect(kube.Apply(ctx, namespace, fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":%q},"spec":{"nodeName":%q,"restartPolicy":"Never",
			"containers":[{"name":"c","image":"curlimages/curl:8.23.0","command":["sh","-c",%q]}]}}`, name, names[1],
			fmt.Sprintf(`sleep 5; curl -s -m 10 -o /dev/null -w "%%{http_code}" http://%s:8080/ || true`, ip)))).To(Succeed())
		waitPodSucceeded(ctx, namespace, name)
		out, err := kube.Kubectl(ctx, namespace, "logs", name)
		Expect(err).NotTo(HaveOccurred())
		return strings.TrimSpace(out)
	}
	since := time.Now().UTC().Format(time.RFC3339)
	Expect(curl(meshed, "neighbour")).To(Equal("200"), "a meshed pod on another node, in the allowed namespace")
	Expect(curl(stranger, "stranger")).To(Equal("000"), "a meshed pod no policy allows")
	Expect(curl(outside, "plaintext")).To(Equal("000"), "a pod outside the mesh, in plaintext")
	logs, err := kube.Kubectl(ctx, "istio-system", "logs", "--selector=app=ztunnel", "--prefix", "--since-time="+since, "--tail=-1")
	Expect(err).NotTo(HaveOccurred())
	source := func(namespace string) string {
		return fmt.Sprintf(`src.identity="spiffe://cluster.local/ns/%s/sa/default"`, namespace)
	}
	Expect(logs).To(ContainSubstring(source(meshed)), "ztunnel did not carry the neighbour's identity")
	Expect(logs).To(MatchRegexp(regexp.QuoteMeta(source(stranger))+`[^\n]*policy rejection|policy rejection[^\n]*`+regexp.QuoteMeta(source(stranger))),
		"ztunnel did not refuse the stranger by policy")

	vip, err := testbedGatewayVIP()
	Expect(err).NotTo(HaveOccurred())
	host := "probe." + suiteDomain
	var answer, issuer []byte
	Eventually(ctx, func() bool {
		answer, err = exec.CommandContext(ctx, "curl", "-sk", "-m", "10", "-o", "/dev/null", "-w", "%{http_code}", "--resolve", host+":80:"+vip, "http://"+host+"/").CombinedOutput()
		return err == nil && string(answer) == "301"
	}).WithTimeout(10*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), func() string {
		return fmt.Sprintf("port 80 at the VIP: %s", answer)
	})
	Eventually(ctx, func() bool {
		issuer, err = exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf(`curl -skv -m 10 -o /dev/null --resolve %s:443:%s https://%s/ 2>&1 | grep -i 'issuer:'`, host, vip, host)).CombinedOutput()
		return err == nil && strings.Contains(string(issuer), "homelab-dev-ca")
	}).WithTimeout(10*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), func() string {
		return fmt.Sprintf("the certificate at the VIP: %s", issuer)
	})
}

// applyTestbedPostgres creates a namespace in the mesh holding CloudNativePG's Cluster db, three instances on Ceph, and
// returns the namespace; it is deleted when the spec ends
func applyTestbedPostgres(ctx context.Context) string {
	GinkgoHelper()
	out, err := exec.CommandContext(ctx, "yq", ".database.cnpg.image", filepath.Join(repo.Root(), "platform/forgejo/values.yaml")).Output()
	Expect(err).NotTo(HaveOccurred())
	image := strings.TrimSpace(string(out))
	ns := fmt.Sprintf("testbed-postgres-%d", time.Now().Unix())
	Expect(kube.Apply(ctx, "", fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q,"labels":{"istio.io/dataplane-mode":"ambient"}}}`, ns))).To(Succeed())
	DeferCleanup(func(ctx SpecContext) { Expect(kube.DeleteNamespace(ctx, ns)).To(Succeed()) }, NodeTimeout(5*time.Minute))
	Expect(kube.Apply(ctx, ns, fmt.Sprintf(`{"apiVersion":"v1","kind":"List","items":[
		{"apiVersion":"security.istio.io/v1","kind":"AuthorizationPolicy","metadata":{"name":"allow","namespace":%[1]q},
			"spec":{"action":"ALLOW","rules":[{"from":[{"source":{"namespaces":[%[1]q]}}]}]}},
		{"apiVersion":"security.istio.io/v1","kind":"AuthorizationPolicy","metadata":{"name":"allow-cnpg-status","namespace":%[1]q},
			"spec":{"action":"ALLOW","selector":{"matchLabels":{"cnpg.io/cluster":"db"}},"rules":[{"to":[{"operation":{"ports":["8000"]}}]}]}},
		{"apiVersion":"security.istio.io/v1","kind":"PeerAuthentication","metadata":{"name":"db-status","namespace":%[1]q},
			"spec":{"selector":{"matchLabels":{"cnpg.io/cluster":"db"}},"mtls":{"mode":"STRICT"},"portLevelMtls":{"8000":{"mode":"PERMISSIVE"}}}}]}`, ns))).To(Succeed())
	cluster := fmt.Sprintf(`{"apiVersion":"postgresql.cnpg.io/v1","kind":"Cluster","metadata":{"name":"db","namespace":%q},
		"spec":{"instances":3,"imageName":%q,"storage":{"size":"1Gi","storageClass":"standard-rwo"}}}`, ns, image)
	// The operator's CRD and webhook may still be coming up on a fresh install
	Eventually(ctx, func() error { return kube.Apply(ctx, ns, cluster) }).WithTimeout(20*time.Minute).WithPolling(15*time.Second).
		Should(Succeed(), "CloudNativePG refuses the Cluster")
	return ns
}

// postgres reads the Cluster db's status and runs psql in its pods
type postgres struct {
	ctx       context.Context
	namespace string
}

func (p postgres) status(field string) string {
	out, _ := kube.Kubectl(p.ctx, p.namespace, "get", "cluster", "db", "-o", "jsonpath={.status."+field+"}")
	return out
}

func (p postgres) psql(pod, query string) (string, error) {
	out, err := kube.Kubectl(p.ctx, p.namespace, "exec", pod, "-c", "postgres", "--", "psql", "-d", "app", "-tAc", query)
	return strings.TrimSpace(out), err
}

// Three CloudNativePG instances across the nodes, in the mesh and on Ceph as Forgejo's database is in production
// (platform/forgejo/templates/database.yaml): the primary's pod goes, a replica on another node takes over and keeps the
// data. This workstation does not hold that layer (docs/installation/testbed.md): it runs where the list names the
// operator
var _ = Describe("TestbedPostgresFailover", Label("testbed", "proof"), func() {
	It("fails over and keeps the data", func(ctx SpecContext) {
		if !strings.Contains(strings.Join(testbedList(ctx), " "), "platform/cloudnative-pg") {
			Skip("system/argocd/values-testbed.yaml does not list platform/cloudnative-pg")
		}
		db := postgres{ctx, applyTestbedPostgres(ctx)}
		allReady := func(message string) {
			Eventually(ctx, func() string { return db.status("readyInstances") }).WithTimeout(30*time.Minute).WithPolling(10*time.Second).
				Should(Equal("3"), message)
		}
		node := func(pod string) string {
			out, err := kube.Kubectl(ctx, db.namespace, "get", "pod", pod, "-o", "jsonpath={.spec.nodeName}")
			Expect(err).NotTo(HaveOccurred())
			return out
		}
		allReady("three instances are not ready")
		primary := db.status("currentPrimary")
		Expect(primary).NotTo(BeEmpty())
		_, err := db.psql(primary, "CREATE TABLE probe (value text); INSERT INTO probe VALUES ('testbed-postgres')")
		Expect(err).NotTo(HaveOccurred())
		pods, err := kube.Kubectl(ctx, db.namespace, "get", "pods", "-l", "cnpg.io/cluster=db,cnpg.io/instanceRole=replica", "-o", "jsonpath={.items[*].metadata.name}")
		Expect(err).NotTo(HaveOccurred())
		replicas := strings.Fields(pods)
		Expect(replicas).To(HaveLen(2))
		// Replication is asynchronous: only a row the replicas hold must survive
		for _, replica := range replicas {
			Eventually(ctx, func() bool {
				value, err := db.psql(replica, "SELECT value FROM probe")
				return err == nil && value == "testbed-postgres"
			}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "%s did not receive the row", replica)
		}

		lost := node(primary)
		out, err := kube.Kubectl(ctx, db.namespace, "delete", "pod", primary, "--wait=false")
		Expect(err).NotTo(HaveOccurred(), out)
		Eventually(ctx, func() bool {
			current := db.status("currentPrimary")
			return current != "" && current != primary && db.status("targetPrimary") == current
		}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "no replica was promoted")
		promoted := db.status("currentPrimary")
		Expect(node(promoted)).NotTo(Equal(lost), "the new primary is on the node that lost the old one")
		Eventually(ctx, func() bool {
			value, err := db.psql(promoted, "INSERT INTO probe VALUES ('after'); SELECT value FROM probe WHERE value = 'testbed-postgres'")
			return err == nil && strings.HasSuffix(value, "testbed-postgres")
		}).WithTimeout(3*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "the promoted %s neither keeps the row nor takes writes", promoted)
		// The old primary comes back as a replica of the new one
		allReady("the old primary did not come back")
		Eventually(ctx, func() bool {
			value, err := db.psql(primary, "SELECT count(*) FROM probe")
			return err == nil && value == "2"
		}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "%s did not catch up", primary)
	}, SpecTimeout(60*time.Minute))
})
