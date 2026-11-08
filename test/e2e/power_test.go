package e2e

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/repo"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Every node losing power, an ordered shutdown, a node not returning (docs/superpowers/specs/2026-11-04-power-design.md). just
// testbed-power runs them one at a time on a healthy testbed. virsh destroy keeps what reached QEMU in WSL's page cache
// (cache=unsafe): these prove the software recovers, not that a real disk keeps what it acknowledged.

const (
	powerNamespace = "power-probe"
	powerLimit     = 30 * time.Minute
	cephConfigPath = "/var/lib/rook/rook-ceph/rook-ceph.config"
)

func powerNamespaceReady(ctx context.Context) {
	GinkgoHelper()
	// A run before this one may still be deleting it
	Eventually(ctx, func() bool {
		_, err := kube.Kubectl(ctx, "", "get", "namespace", powerNamespace)
		return err != nil && strings.Contains(err.Error(), "NotFound")
	}).WithTimeout(10*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "%s is still there", powerNamespace)
	Expect(kube.Apply(ctx, "", fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q,"labels":{"istio.io/dataplane-mode":"none"}}}`, powerNamespace))).To(Succeed())
	DeferCleanup(func(ctx SpecContext) { Expect(kube.DeleteNamespace(ctx, powerNamespace)).To(Succeed()) }, NodeTimeout(10*time.Minute))
}

// powerProbe appends numbered records to a file on a Ceph volume, flushing each before printing its number. What the spec
// read from its log, from outside the VMs, was acknowledged.
type powerProbe struct {
	cancel  context.CancelFunc
	mu      sync.Mutex
	numbers []string
}

const powerProbeScript = `touch /data/log
[ -z "$(tail -c 1 /data/log)" ] || echo >> /data/log
i=$(tail -n 1 /data/log)
i=${i:-0}
while :; do
	i=$((i + 1))
	echo "$i" | dd of=/data/log oflag=append conv=notrunc,fsync 2>/dev/null && echo "$i"
	sleep 0.2
done`

func probeAvailable(ctx context.Context) string {
	out, _ := kube.Kubectl(ctx, powerNamespace, "get", "deployment", "probe", "-o", "jsonpath={.status.availableReplicas}")
	return out
}

func startPowerProbe(ctx context.Context) *powerProbe {
	GinkgoHelper()
	Expect(kube.Apply(ctx, powerNamespace, `{"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"name":"data"},
		"spec":{"accessModes":["ReadWriteOnce"],"storageClassName":"standard-rwo","resources":{"requests":{"storage":"1Gi"}}}}`)).To(Succeed())
	Expect(kube.Apply(ctx, powerNamespace, fmt.Sprintf(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"probe"},"spec":{"replicas":1,"strategy":{"type":"Recreate"},
		"selector":{"matchLabels":{"app":"probe"}},"template":{"metadata":{"labels":{"app":"probe"}},"spec":{
		"containers":[{"name":"c","image":"busybox:1.37.0","command":["sh","-c",%q],"volumeMounts":[{"name":"v","mountPath":"/data"}]}],
		"volumes":[{"name":"v","persistentVolumeClaim":{"claimName":"data"}}]}}}}`, powerProbeScript))).To(Succeed())
	Eventually(ctx, func() string { return probeAvailable(ctx) }).WithTimeout(5*time.Minute).WithPolling(5*time.Second).
		Should(Equal("1"), "the probe did not start")

	logsCtx, cancel := context.WithCancel(ctx)
	DeferCleanup(cancel)
	probe := &powerProbe{cancel: cancel}
	logs := exec.CommandContext(logsCtx, "kubectl", "-n", powerNamespace, "logs", "-f", "deploy/probe")
	stdout, err := logs.StdoutPipe()
	Expect(err).NotTo(HaveOccurred())
	Expect(logs.Start()).To(Succeed())
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			probe.mu.Lock()
			probe.numbers = append(probe.numbers, scanner.Text())
			probe.mu.Unlock()
		}
		_ = logs.Wait()
	}()
	Eventually(ctx, func() int { return len(probe.acked()) }).WithTimeout(5*time.Minute).WithPolling(time.Second).
		Should(BeNumerically(">=", 50), "the probe acknowledged too little")
	return probe
}

func (p *powerProbe) acked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.numbers)
}

func (p *powerProbe) file(ctx context.Context) []string {
	GinkgoHelper()
	out, err := kube.Kubectl(ctx, powerNamespace, "exec", "deploy/probe", "--", "cat", "/data/log")
	Expect(err).NotTo(HaveOccurred())
	return strings.Fields(out)
}

// verify: everything acknowledged is in the file, in order, and the probe writes again after the file's end at recovery
func (p *powerProbe) verify(ctx context.Context, acked []string) {
	GinkgoHelper()
	Expect(acked).NotTo(BeEmpty())
	Eventually(ctx, func() string { return probeAvailable(ctx) }).WithTimeout(10*time.Minute).WithPolling(10*time.Second).
		Should(Equal("1"), "the probe did not return")
	lines := p.file(ctx)
	next := 0
	for _, line := range lines {
		if next < len(acked) && line == acked[next] {
			next++
		}
	}
	Expect(next).To(Equal(len(acked)), "%d acknowledged records; missing from %s on", len(acked), acked[min(next, len(acked)-1)])
	before := len(lines)
	Eventually(ctx, func() int { return len(p.file(ctx)) }).WithTimeout(2*time.Minute).WithPolling(5*time.Second).
		Should(BeNumerically(">", before+5), "the probe does not write again")
	GinkgoWriter.Printf("%d acknowledged records intact, %d in the file\n", len(acked), before)
}

// cephAnswersHealthy asks Ceph itself: the CephCluster's status still says what the operator saw before a cut
func cephAnswersHealthy(ctx context.Context, osds int) bool {
	ceph := func(args ...string) string {
		out, err := kube.Kubectl(ctx, "rook-ceph", append([]string{"exec", "deploy/rook-ceph-operator", "--", "ceph", "--conf=" + cephConfigPath}, args...)...)
		if err != nil {
			return ""
		}
		return out
	}
	if !strings.HasPrefix(ceph("health"), "HEALTH_OK") || !strings.Contains(ceph("osd", "stat"), fmt.Sprintf("%d up", osds)) {
		return false
	}
	// Ceph counts a dead OSD as up for a while after a cut: only the OSD itself tells
	for _, id := range strings.Fields(ceph("osd", "ls")) {
		if ceph("tell", "osd."+id, "version") == "" {
			return false
		}
	}
	return true
}

func powerHealthy(ctx context.Context, wait time.Duration) {
	GinkgoHelper()
	names, _ := testbedHosts()
	Eventually(ctx, func() bool {
		if !cephAnswersHealthy(ctx, len(names)) {
			return false
		}
		ready, err := kube.Kubectl(ctx, "", "get", "nodes", "-o", `jsonpath={.items[*].status.conditions[?(@.type=="Ready")].status}`)
		return err == nil && ready == strings.TrimSpace(strings.Repeat("True ", len(names)))
	}).WithTimeout(wait).WithPolling(15*time.Second).Should(BeTrue(), "the testbed is not healthy")
}

func bootIDs(ctx context.Context) map[string]string {
	GinkgoHelper()
	names, _ := testbedHosts()
	ids := map[string]string{}
	for _, name := range names {
		id, err := kube.Kubectl(ctx, "", "get", "node", name, "-o", "jsonpath={.status.nodeInfo.bootID}")
		Expect(err).NotTo(HaveOccurred())
		ids[name] = id
	}
	return ids
}

// powerRecovery requires each condition within what is left of powerLimit, and logs when it held
type powerRecovery struct {
	ctx   context.Context
	start time.Time
}

func (r powerRecovery) reached(what string, check func() bool) {
	GinkgoHelper()
	left := powerLimit - time.Since(r.start)
	Expect(left).To(BeNumerically(">", 0), "out of time before %s", what)
	Eventually(r.ctx, check).WithTimeout(left).WithPolling(10*time.Second).Should(BeTrue(), "%s, within %s", what, powerLimit)
	GinkgoWriter.Printf("%s after %s\n", what, time.Since(r.start).Round(time.Second))
}

// nodesBack waits for the API at its VIP and a new boot on each node: until a kubelet reports, the API server shows what it knew
func (r powerRecovery) nodesBack(booted map[string]string, names ...string) {
	GinkgoHelper()
	r.reached("the API answers at its VIP", func() bool {
		out, err := kube.Kubectl(r.ctx, "", "get", "--raw", "/readyz")
		return err == nil && out == "ok"
	})
	for _, name := range names {
		r.reached(name+" on a new boot and Ready", func() bool {
			out, err := kube.Kubectl(r.ctx, "", "get", "node", name,
				"-o", `jsonpath={.status.nodeInfo.bootID} {.status.conditions[?(@.type=="Ready")].status}`)
			fields := strings.Fields(out)
			return err == nil && len(fields) == 2 && fields[0] != booted[name] && fields[1] == "True"
		})
	}
}

// gatewayAnswers is the check that would catch 12a-2's finding: kube-vip holding the Gateway's lease with the VIP silent
func gatewayAnswers(ctx context.Context) bool {
	vip, err := testbedGatewayVIP()
	if err != nil {
		return false
	}
	host := "probe." + suiteDomain
	answer, err := exec.CommandContext(ctx, "curl", "-sk", "-m", "10", "-o", "/dev/null", "-w", "%{http_code}", "--resolve", host+":80:"+vip, "http://"+host+"/").Output()
	return err == nil && string(answer) == "301"
}

func dumpKubeVIP(ctx context.Context) string {
	var dump strings.Builder
	for _, args := range [][]string{
		{"-n", "istio-system", "get", "service", "gateway-istio", "-o", "jsonpath={.status}"},
		{"-n", "kube-system", "get", "leases", "-o", `jsonpath={range .items[*]}{.metadata.name}={.spec.holderIdentity}{"\n"}{end}`},
		{"-n", "kube-system", "logs", "-l", "app=kube-vip", "--prefix", "--tail=60"},
	} {
		out, err := kube.Kubectl(ctx, "", args...)
		fmt.Fprintf(&dump, "kubectl %s: %v\n%s\n", strings.Join(args, " "), err, out)
	}
	return dump.String()
}

func cutEveryNode(ctx context.Context) {
	GinkgoHelper()
	names, _ := testbedHosts()
	var group sync.WaitGroup
	failed := make([]string, len(names))
	for i, name := range names {
		group.Add(1)
		go func() {
			defer group.Done()
			// Collected, not asserted: not the spec's goroutine
			if out, err := exec.CommandContext(ctx, "virsh", "-c", "qemu:///system", "destroy", name).CombinedOutput(); err != nil {
				failed[i] = fmt.Sprintf("virsh destroy %s: %v %s", name, err, out)
			}
		}()
	}
	group.Wait()
	for _, name := range names {
		virsh(ctx, "start", name)
	}
	Expect(slices.DeleteFunc(failed, func(s string) bool { return s == "" })).To(BeEmpty())
}

// Every node loses power at once while a write is under way: all of it returns with no command run on the cluster
var _ = Describe("PowerCutEveryNode", Label("testbed", "power", "cut-every-node"), func() {
	It("recovers from a cut of every node", func(ctx SpecContext) {
		powerHealthy(ctx, 30*time.Minute)
		names, _ := testbedHosts()
		powerNamespaceReady(ctx)
		probe := startPowerProbe(ctx)
		booted := bootIDs(ctx)

		cutEveryNode(ctx)
		start := time.Now()
		acked := probe.acked()
		probe.cancel()
		GinkgoWriter.Printf("cut with %d records acknowledged\n", len(acked))

		recovery := powerRecovery{ctx, start}
		recovery.nodesBack(booted, names...)
		recovery.reached("every node a member of etcd", func() bool {
			out, err := kube.Kubectl(ctx, "", "get", "nodes", "-o", `jsonpath={.items[*].metadata.annotations.etcd\.k3s\.cattle\.io/node-name}`)
			return err == nil && len(strings.Fields(out)) == len(names)
		})
		recovery.reached("Ceph HEALTH_OK with every OSD up", func() bool { return cephAnswersHealthy(ctx, len(names)) })
		Eventually(ctx, func() bool { return gatewayAnswers(ctx) }).WithTimeout(max(powerLimit-time.Since(start), time.Minute)).WithPolling(10*time.Second).
			Should(BeTrue(), func() string {
				return "the Gateway's VIP did not answer: is this 12a-2's kube-vip finding (its lease held, the Service's status empty)?\n" + dumpKubeVIP(ctx)
			})
		GinkgoWriter.Printf("the Gateway answers at its VIP after %s\n", time.Since(start).Round(time.Second))
		probe.verify(ctx, acked)
		testbedMeshWorks(ctx)
		GinkgoWriter.Printf("recovered and checked in %s\n", time.Since(start).Round(time.Second))
	}, SpecTimeout(90*time.Minute))
})

func powerFixtures(ctx context.Context) {
	GinkgoHelper()
	cron := func(name string, suspend bool) string {
		return fmt.Sprintf(`{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"name":%q},"spec":{"schedule":"0 0 1 1 *","suspend":%t,
			"jobTemplate":{"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"c","image":"busybox:1.37.0","command":["true"]}]}}}}}}`, name, suspend)
	}
	Expect(kube.Apply(ctx, powerNamespace, cron("running", false))).To(Succeed())
	Expect(kube.Apply(ctx, powerNamespace, cron("held", true))).To(Succeed())
}

func cronSuspended(ctx context.Context, namespace, name string) string {
	out, _ := kube.Kubectl(ctx, namespace, "get", "cronjob", name, "-o", "jsonpath={.spec.suspend}")
	return out
}

func markerGone(ctx context.Context) bool {
	_, err := kube.Kubectl(ctx, "kube-system", "get", "configmap", "cluster-shutdown")
	return err != nil && strings.Contains(err.Error(), "NotFound")
}

func labCluster(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "scripts/lab", append([]string{"cluster"}, args...)...)
	cmd.Dir = repo.Root()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func nooutSet(ctx context.Context) (bool, error) {
	out, err := kube.Kubectl(ctx, "rook-ceph", "exec", "deploy/rook-ceph-operator", "--", "ceph", "--conf="+cephConfigPath, "osd", "dump")
	return strings.Contains(out, "noout"), err
}

func controllerReplicas(ctx context.Context) string {
	out, _ := kube.Kubectl(ctx, "argocd", "get", "statefulset", "argocd-application-controller", "-o", "jsonpath={.spec.replicas}")
	return out
}

// A poweroff stops the node's services and leaves that in its journal, unless the deadline cut it: at most one node, the one
// holding the probe's volume, may have needed that
func stoppedInOrder(ctx context.Context) {
	GinkgoHelper()
	_, ips := testbedHosts()
	stopped := 0
	for _, ip := range ips {
		out, err := testbedSSH(ctx, ip, "journalctl -b -1 -q --no-pager --grep 'Stopped target Multi-User' | wc -l")
		Expect(err).NotTo(HaveOccurred(), out)
		if out != "0" {
			stopped++
		}
	}
	GinkgoWriter.Printf("%d of %d nodes stopped their services before the power went\n", stopped, len(ips))
	Expect(stopped).To(BeNumerically(">=", len(ips)-1), "the poweroff cut the nodes instead of stopping them")
}

func waitAllShutOff(ctx context.Context, names []string) {
	GinkgoHelper()
	start := time.Now()
	Eventually(ctx, func() bool {
		for _, name := range names {
			if domstate(ctx, name) != "shut off" {
				return false
			}
		}
		return true
	}).WithTimeout(4*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), "a node did not power off")
	GinkgoWriter.Printf("every node off after %s\n", time.Since(start).Round(time.Second))
}

// An ordered shutdown stays off until someone starts the nodes; they then undo it by themselves, and only what it changed
var _ = Describe("PowerShutdownStartup", Label("testbed", "power", "shutdown-startup"), func() {
	It("undoes an ordered shutdown by itself", func(ctx SpecContext) {
		powerHealthy(ctx, 30*time.Minute)
		names, _ := testbedHosts()
		powerNamespaceReady(ctx)
		probe := startPowerProbe(ctx)
		powerFixtures(ctx)
		Expect(kube.Apply(ctx, powerNamespace, `{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":"long"},"spec":{"template":{"spec":{"restartPolicy":"Never",
		"containers":[{"name":"c","image":"busybox:1.37.0","command":["sleep","3600"]}]}}}}`)).To(Succeed())
		replicas := controllerReplicas(ctx)
		booted := bootIDs(ctx)

		out, err := labCluster(ctx, "shutdown", "--environment", "testbed", "--job-wait=1m")
		Expect(err).NotTo(HaveOccurred(), out)
		Expect(out).To(ContainSubstring(powerNamespace+"/long"), "it did not name the Job it left")
		acked := probe.acked()
		probe.cancel()
		waitAllShutOff(ctx, names)
		sleep(ctx, time.Minute)
		for _, name := range names {
			Expect(domstate(ctx, name)).To(Equal("shut off"), "%s came back by itself", name)
		}

		for _, name := range names {
			virsh(ctx, "start", name)
		}
		recovery := powerRecovery{ctx, time.Now()}
		recovery.nodesBack(booted, names...)
		stoppedInOrder(ctx)
		recovery.reached("the marker gone", func() bool { return markerGone(ctx) })
		Expect(cronSuspended(ctx, powerNamespace, "running")).To(Equal("false"), "the CronJob it suspended stays suspended")
		Expect(cronSuspended(ctx, powerNamespace, "held")).To(Equal("true"), "the CronJob suspended on purpose was resumed")
		Expect(cronSuspended(ctx, "istio-system", "istio-cni-watchdog")).NotTo(Equal("true"), "the watchdog is suspended")
		noout, err := nooutSet(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(noout).To(BeFalse(), "noout is still set")
		Expect(controllerReplicas(ctx)).To(Equal(replicas), "Argo CD's controller did not return")
		recovery.reached("every Application Synced", func() bool {
			out, err := kube.Kubectl(ctx, "argocd", "get", "applications", "-o", "jsonpath={.items[*].status.sync.status}")
			return err == nil && out != "" && !strings.Contains(out, "OutOfSync") && !strings.Contains(out, "Unknown")
		})
		probe.verify(ctx, acked)

		out, err = labCluster(ctx, "startup", "--environment", "testbed")
		Expect(err).NotTo(HaveOccurred(), "with no marker: %s", out)
	}, SpecTimeout(90*time.Minute))
})

// A node that never returns must not leave backups and rotation off, and Ceph stops waiting for it after 30 minutes
var _ = Describe("PowerNodeNotReturning", Label("testbed", "power", "node-not-returning"), func() {
	It("comes back without one node", func(ctx SpecContext) {
		powerHealthy(ctx, 30*time.Minute)
		names, _ := testbedHosts()
		missing, returning := names[len(names)-1], names[:len(names)-1]
		powerNamespaceReady(ctx)
		powerFixtures(ctx)
		replicas := controllerReplicas(ctx)
		booted := bootIDs(ctx)

		out, err := labCluster(ctx, "shutdown", "--environment", "testbed", "--job-wait=1m")
		Expect(err).NotTo(HaveOccurred(), out)
		waitAllShutOff(ctx, names)
		// Whatever happens below, the testbed ends with its three nodes
		DeferCleanup(func(ctx SpecContext) {
			if domstate(ctx, missing) != "running" {
				virsh(ctx, "start", missing)
			}
			powerHealthy(ctx, 20*time.Minute)
		}, NodeTimeout(25*time.Minute))
		for _, name := range returning {
			virsh(ctx, "start", name)
		}
		recovery := powerRecovery{ctx, time.Now()}
		recovery.nodesBack(booted, returning...)
		recovery.reached("the CronJob resumed without "+missing, func() bool { return cronSuspended(ctx, powerNamespace, "running") == "false" })
		recovery.reached("Argo CD's controller back without "+missing, func() bool { return controllerReplicas(ctx) == replicas })
		Expect(markerGone(ctx)).To(BeFalse(), "noout came off while an OSD is away: Ceph should wait up to 30 minutes first")

		// noout's 30 minutes count from startup's first run, with a margin
		limit := powerRecovery{ctx, recovery.start.Add(40*time.Minute - powerLimit)}
		limit.reached("the marker gone", func() bool { return markerGone(ctx) })
		gone := time.Since(recovery.start)
		GinkgoWriter.Printf("the marker lasted %s\n", gone.Round(time.Second))
		Expect(gone).To(BeNumerically(">=", 29*time.Minute), "noout came off before its limit with an OSD away")
		noout, err := nooutSet(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(noout).To(BeFalse(), "noout is still set")
	}, SpecTimeout(90*time.Minute))
})

// One measurement (docs/installation/testbed.md), not part of just testbed-power: CloudNativePG installed by hand on a stable layer 2
var _ = Describe("PowerCutPostgres", Label("testbed", "power", "cut-postgres"), func() {
	It("keeps an acknowledged write through a cut", func(ctx SpecContext) {
		if _, err := kube.Kubectl(ctx, "", "get", "crd", "clusters.postgresql.cnpg.io"); err != nil {
			Skip("CloudNativePG is not installed: this is a one-off measurement")
		}
		powerHealthy(ctx, 30*time.Minute)
		db := postgres{ctx, applyTestbedPostgres(ctx)}
		Eventually(ctx, func() string { return db.status("readyInstances") }).WithTimeout(30*time.Minute).WithPolling(10*time.Second).
			Should(Equal("3"), "three instances are not ready")
		primary := db.status("currentPrimary")
		_, err := db.psql(primary, "CREATE TABLE probe (value text); INSERT INTO probe VALUES ('power-cut')")
		Expect(err).NotTo(HaveOccurred())
		names, _ := testbedHosts()
		booted := bootIDs(ctx)

		cutEveryNode(ctx)
		start := time.Now()
		powerRecovery{ctx, start}.nodesBack(booted, names...)
		// Postgres itself tells: the Cluster's status still says what the operator saw before the cut
		Eventually(ctx, func() bool {
			value, err := db.psql(db.status("currentPrimary"), "SELECT value FROM probe")
			return err == nil && value == "power-cut"
		}).WithTimeout(45*time.Minute-time.Since(start)).WithPolling(15*time.Second).Should(BeTrue(), "no primary returned with the write")
		GinkgoWriter.Printf("a primary has the write after %s\n", time.Since(start).Round(time.Second))
		Eventually(ctx, func() bool {
			replicas, err := db.psql(db.status("currentPrimary"), "SELECT count(*) FROM pg_stat_replication WHERE state = 'streaming'")
			return err == nil && replicas == "2"
		}).WithTimeout(45*time.Minute-time.Since(start)).WithPolling(15*time.Second).Should(BeTrue(), "the replicas did not return")
		GinkgoWriter.Printf("two replicas streaming after %s\n", time.Since(start).Round(time.Second))
		GinkgoWriter.Printf("primary %s before, %s after\n", primary, db.status("currentPrimary"))
	}, SpecTimeout(120*time.Minute))
})
