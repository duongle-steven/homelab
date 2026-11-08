package e2e

import (
	"bufio"
	"context"
	"fmt"
	"github.com/duongle-steven/homelab/test/internal/repo"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/testenv"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every node losing power, an ordered shutdown, a node not returning (docs/superpowers/specs/2026-11-04-power-design.md). just
// testbed-power runs them one at a time on a healthy testbed. virsh destroy keeps what reached QEMU in WSL's page cache
// (cache=unsafe): these prove the software recovers, not that a real disk keeps what it acknowledged.

const (
	powerNamespace = "power-probe"
	powerLimit     = 30 * time.Minute
	cephConfigPath = "/var/lib/rook/rook-ceph/rook-ceph.config"
)

func powerNamespaceReady(t *testing.T) *k8s.KubectlOptions {
	t.Helper()
	options := testbedKubectl(powerNamespace)
	// A run before this one may still be deleting it
	require.Eventually(t, func() bool {
		_, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "namespace", powerNamespace)
		return err != nil && strings.Contains(err.Error(), "NotFound")
	}, 10*time.Minute, 5*time.Second, "%s is still there", powerNamespace)
	k8s.KubectlApplyFromString(t, options, fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q,"labels":{"istio.io/dataplane-mode":"none"}}}`, powerNamespace))
	t.Cleanup(func() { k8s.DeleteNamespace(t, options, powerNamespace) })
	return options
}

// powerProbe appends numbered records to a file on a Ceph volume, flushing each before printing its number. What the test
// read from its log, from outside the VMs, was acknowledged.
type powerProbe struct {
	options *k8s.KubectlOptions
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

func startPowerProbe(t *testing.T, options *k8s.KubectlOptions) *powerProbe {
	t.Helper()
	k8s.KubectlApplyFromString(t, options, `{"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"name":"data"},
		"spec":{"accessModes":["ReadWriteOnce"],"storageClassName":"standard-rwo","resources":{"requests":{"storage":"1Gi"}}}}`)
	k8s.KubectlApplyFromString(t, options, fmt.Sprintf(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"probe"},"spec":{"replicas":1,"strategy":{"type":"Recreate"},
		"selector":{"matchLabels":{"app":"probe"}},"template":{"metadata":{"labels":{"app":"probe"}},"spec":{
		"containers":[{"name":"c","image":"busybox:1.37.0","command":["sh","-c",%q],"volumeMounts":[{"name":"v","mountPath":"/data"}]}],
		"volumes":[{"name":"v","persistentVolumeClaim":{"claimName":"data"}}]}}}}`, powerProbeScript))
	require.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "deployment", "probe", "-o", "jsonpath={.status.availableReplicas}")
		return err == nil && out == "1"
	}, 5*time.Minute, 5*time.Second, "the probe did not start")

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	probe := &powerProbe{options: options, cancel: cancel}
	logs := exec.CommandContext(ctx, "kubectl", "-n", powerNamespace, "logs", "-f", "deploy/probe")
	stdout, err := logs.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, logs.Start())
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			probe.mu.Lock()
			probe.numbers = append(probe.numbers, scanner.Text())
			probe.mu.Unlock()
		}
		_ = logs.Wait()
	}()
	require.Eventually(t, func() bool { return len(probe.acked()) >= 50 }, 5*time.Minute, time.Second, "the probe acknowledged too little")
	return probe
}

func (p *powerProbe) acked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.numbers)
}

func (p *powerProbe) file(t *testing.T) []string {
	t.Helper()
	out, err := k8s.RunKubectlAndGetOutputE(t, p.options, "exec", "deploy/probe", "--", "cat", "/data/log")
	require.NoError(t, err)
	return strings.Fields(out)
}

// verify: everything acknowledged is in the file, in order, and the probe writes again after the file's end at recovery
func (p *powerProbe) verify(t *testing.T, acked []string) {
	t.Helper()
	require.NotEmpty(t, acked)
	var lines []string
	require.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, p.options, "get", "deployment", "probe", "-o", "jsonpath={.status.availableReplicas}")
		return err == nil && out == "1"
	}, 10*time.Minute, 10*time.Second, "the probe did not return")
	lines = p.file(t)
	next := 0
	for _, line := range lines {
		if next < len(acked) && line == acked[next] {
			next++
		}
	}
	require.Equal(t, len(acked), next, "%d acknowledged records; missing from %s on", len(acked), acked[min(next, len(acked)-1)])
	before := len(lines)
	assert.Eventually(t, func() bool { return len(p.file(t)) > before+5 }, 2*time.Minute, 5*time.Second, "the probe does not write again")
	t.Logf("%d acknowledged records intact, %d in the file", len(acked), before)
}

// cephAnswersHealthy asks Ceph itself: the CephCluster's status still says what the operator saw before a cut
func cephAnswersHealthy(t *testing.T, osds int) bool {
	ceph := func(args ...string) string {
		out, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl("rook-ceph"), append([]string{"exec", "deploy/rook-ceph-operator", "--", "ceph", "--conf=" + cephConfigPath}, args...)...)
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

func powerHealthy(t *testing.T, wait time.Duration) {
	t.Helper()
	names, _ := testbedHosts(t)
	require.Eventually(t, func() bool {
		if !cephAnswersHealthy(t, len(names)) {
			return false
		}
		ready, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl(""), "get", "nodes", "-o", `jsonpath={.items[*].status.conditions[?(@.type=="Ready")].status}`)
		return err == nil && ready == strings.TrimSpace(strings.Repeat("True ", len(names)))
	}, wait, 15*time.Second, "the testbed is not healthy")
}

func bootIDs(t *testing.T) map[string]string {
	t.Helper()
	names, _ := testbedHosts(t)
	ids := map[string]string{}
	for _, name := range names {
		id, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl(""), "get", "node", name, "-o", "jsonpath={.status.nodeInfo.bootID}")
		require.NoError(t, err)
		ids[name] = id
	}
	return ids
}

// powerRecovery requires each condition within what is left of powerLimit, and logs when it held
type powerRecovery struct {
	t     *testing.T
	start time.Time
}

func (r powerRecovery) reached(what string, check func() bool) {
	r.t.Helper()
	left := powerLimit - time.Since(r.start)
	require.Greater(r.t, left, time.Duration(0), "out of time before %s", what)
	require.Eventually(r.t, check, left, 10*time.Second, "%s, within %s", what, powerLimit)
	r.t.Logf("%s after %s", what, time.Since(r.start).Round(time.Second))
}

// nodesBack waits for the API at its VIP and a new boot on each node: until a kubelet reports, the API server shows what it knew
func (r powerRecovery) nodesBack(booted map[string]string, names ...string) {
	r.t.Helper()
	r.reached("the API answers at its VIP", func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(r.t, testbedKubectl(""), "get", "--raw", "/readyz")
		return err == nil && out == "ok"
	})
	for _, name := range names {
		r.reached(name+" on a new boot and Ready", func() bool {
			out, err := k8s.RunKubectlAndGetOutputE(r.t, testbedKubectl(""), "get", "node", name,
				"-o", `jsonpath={.status.nodeInfo.bootID} {.status.conditions[?(@.type=="Ready")].status}`)
			fields := strings.Fields(out)
			return err == nil && len(fields) == 2 && fields[0] != booted[name] && fields[1] == "True"
		})
	}
}

// gatewayAnswers is the check that would catch 12a-2's finding: kube-vip holding the Gateway's lease with the VIP silent
func gatewayAnswers(t *testing.T) bool {
	vip, err := testbedGatewayVIP()
	if err != nil {
		return false
	}
	host := "probe." + testenv.SuiteDomain(t)
	answer, err := exec.Command("curl", "-sk", "-m", "10", "-o", "/dev/null", "-w", "%{http_code}", "--resolve", host+":80:"+vip, "http://"+host+"/").Output()
	return err == nil && string(answer) == "301"
}

func dumpKubeVIP(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{
		{"-n", "istio-system", "get", "service", "gateway-istio", "-o", "jsonpath={.status}"},
		{"-n", "kube-system", "get", "leases", "-o", `jsonpath={range .items[*]}{.metadata.name}={.spec.holderIdentity}{"\n"}{end}`},
		{"-n", "kube-system", "logs", "-l", "app=kube-vip", "--prefix", "--tail=60"},
	} {
		out, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl(""), args...)
		t.Logf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func cutEveryNode(t *testing.T) {
	t.Helper()
	names, _ := testbedHosts(t)
	var group sync.WaitGroup
	for _, name := range names {
		group.Add(1)
		go func() {
			defer group.Done()
			// assert, not require: not the test's goroutine
			out, err := exec.Command("virsh", "-c", "qemu:///system", "destroy", name).CombinedOutput()
			assert.NoError(t, err, string(out))
		}()
	}
	group.Wait()
	for _, name := range names {
		virsh(t, "start", name)
	}
}

// Every node loses power at once while a write is under way: all of it returns with no command run on the cluster
func TestPowerCutEveryNode(t *testing.T) {
	testbedOnly(t)
	powerHealthy(t, 30*time.Minute)
	names, _ := testbedHosts(t)
	probe := startPowerProbe(t, powerNamespaceReady(t))
	booted := bootIDs(t)

	cutEveryNode(t)
	start := time.Now()
	acked := probe.acked()
	probe.cancel()
	t.Logf("cut with %d records acknowledged", len(acked))

	recovery := powerRecovery{t, start}
	recovery.nodesBack(booted, names...)
	recovery.reached("every node a member of etcd", func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl(""), "get", "nodes", "-o", `jsonpath={.items[*].metadata.annotations.etcd\.k3s\.cattle\.io/node-name}`)
		return err == nil && len(strings.Fields(out)) == len(names)
	})
	recovery.reached("Ceph HEALTH_OK with every OSD up", func() bool { return cephAnswersHealthy(t, len(names)) })
	if !assert.Eventually(t, func() bool { return gatewayAnswers(t) }, max(powerLimit-time.Since(start), time.Minute), 10*time.Second,
		"the Gateway's VIP did not answer: is this 12a-2's kube-vip finding (its lease held, the Service's status empty)?") {
		dumpKubeVIP(t)
		t.FailNow()
	}
	t.Logf("the Gateway answers at its VIP after %s", time.Since(start).Round(time.Second))
	probe.verify(t, acked)
	testbedMeshWorks(t)
	t.Logf("recovered and checked in %s", time.Since(start).Round(time.Second))
}

func powerFixtures(t *testing.T, options *k8s.KubectlOptions) {
	t.Helper()
	cron := func(name string, suspend bool) string {
		return fmt.Sprintf(`{"apiVersion":"batch/v1","kind":"CronJob","metadata":{"name":%q},"spec":{"schedule":"0 0 1 1 *","suspend":%t,
			"jobTemplate":{"spec":{"template":{"spec":{"restartPolicy":"Never","containers":[{"name":"c","image":"busybox:1.37.0","command":["true"]}]}}}}}}`, name, suspend)
	}
	k8s.KubectlApplyFromString(t, options, cron("running", false))
	k8s.KubectlApplyFromString(t, options, cron("held", true))
}

func cronSuspended(t *testing.T, namespace, name string) string {
	out, _ := k8s.RunKubectlAndGetOutputE(t, testbedKubectl(namespace), "get", "cronjob", name, "-o", "jsonpath={.spec.suspend}")
	return out
}

func markerGone(t *testing.T) bool {
	_, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl("kube-system"), "get", "configmap", "cluster-shutdown")
	return err != nil && strings.Contains(err.Error(), "NotFound")
}

func labCluster(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("scripts/lab", append([]string{"cluster"}, args...)...)
	cmd.Dir = repo.Root()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func nooutSet(t *testing.T) (bool, error) {
	out, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl("rook-ceph"), "exec", "deploy/rook-ceph-operator", "--", "ceph", "--conf="+cephConfigPath, "osd", "dump")
	return strings.Contains(out, "noout"), err
}

func controllerReplicas(t *testing.T) string {
	out, _ := k8s.RunKubectlAndGetOutputE(t, testbedKubectl("argocd"), "get", "statefulset", "argocd-application-controller", "-o", "jsonpath={.spec.replicas}")
	return out
}

// A poweroff stops the node's services and leaves that in its journal, unless the deadline cut it: at most one node, the one
// holding the probe's volume, may have needed that
func stoppedInOrder(t *testing.T) {
	t.Helper()
	_, ips := testbedHosts(t)
	stopped := 0
	for _, ip := range ips {
		out, err := testbedSSH(t, ip, "journalctl -b -1 -q --no-pager --grep 'Stopped target Multi-User' | wc -l")
		require.NoError(t, err, out)
		if out != "0" {
			stopped++
		}
	}
	t.Logf("%d of %d nodes stopped their services before the power went", stopped, len(ips))
	assert.GreaterOrEqual(t, stopped, len(ips)-1, "the poweroff cut the nodes instead of stopping them")
}

func waitAllShutOff(t *testing.T, names []string) {
	t.Helper()
	start := time.Now()
	require.Eventually(t, func() bool {
		for _, name := range names {
			if domstate(name) != "shut off" {
				return false
			}
		}
		return true
	}, 4*time.Minute, 5*time.Second, "a node did not power off")
	t.Logf("every node off after %s", time.Since(start).Round(time.Second))
}

// An ordered shutdown stays off until someone starts the nodes; they then undo it by themselves, and only what it changed
func TestPowerShutdownStartup(t *testing.T) {
	testbedOnly(t)
	powerHealthy(t, 30*time.Minute)
	names, _ := testbedHosts(t)
	options := powerNamespaceReady(t)
	probe := startPowerProbe(t, options)
	powerFixtures(t, options)
	k8s.KubectlApplyFromString(t, options, `{"apiVersion":"batch/v1","kind":"Job","metadata":{"name":"long"},"spec":{"template":{"spec":{"restartPolicy":"Never",
		"containers":[{"name":"c","image":"busybox:1.37.0","command":["sleep","3600"]}]}}}}`)
	replicas := controllerReplicas(t)
	booted := bootIDs(t)

	out, err := labCluster(t, "shutdown", "--environment", "testbed", "--job-wait=1m")
	require.NoError(t, err, out)
	assert.Contains(t, out, powerNamespace+"/long", "it did not name the Job it left")
	acked := probe.acked()
	probe.cancel()
	waitAllShutOff(t, names)
	time.Sleep(time.Minute)
	for _, name := range names {
		assert.Equal(t, "shut off", domstate(name), "%s came back by itself", name)
	}

	for _, name := range names {
		virsh(t, "start", name)
	}
	recovery := powerRecovery{t, time.Now()}
	recovery.nodesBack(booted, names...)
	stoppedInOrder(t)
	recovery.reached("the marker gone", func() bool { return markerGone(t) })
	assert.Equal(t, "false", cronSuspended(t, powerNamespace, "running"), "the CronJob it suspended stays suspended")
	assert.Equal(t, "true", cronSuspended(t, powerNamespace, "held"), "the CronJob suspended on purpose was resumed")
	assert.NotEqual(t, "true", cronSuspended(t, "istio-system", "istio-cni-watchdog"), "the watchdog is suspended")
	noout, err := nooutSet(t)
	assert.NoError(t, err)
	assert.False(t, noout, "noout is still set")
	assert.Equal(t, replicas, controllerReplicas(t), "Argo CD's controller did not return")
	recovery.reached("every Application Synced", func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl("argocd"), "get", "applications", "-o", "jsonpath={.items[*].status.sync.status}")
		return err == nil && out != "" && !strings.Contains(out, "OutOfSync") && !strings.Contains(out, "Unknown")
	})
	probe.verify(t, acked)

	out, err = labCluster(t, "startup", "--environment", "testbed")
	assert.NoError(t, err, "with no marker: %s", out)
}

// A node that never returns must not leave backups and rotation off, and Ceph stops waiting for it after 30 minutes
func TestPowerNodeNotReturning(t *testing.T) {
	testbedOnly(t)
	powerHealthy(t, 30*time.Minute)
	names, _ := testbedHosts(t)
	missing, returning := names[len(names)-1], names[:len(names)-1]
	options := powerNamespaceReady(t)
	powerFixtures(t, options)
	replicas := controllerReplicas(t)
	booted := bootIDs(t)

	out, err := labCluster(t, "shutdown", "--environment", "testbed", "--job-wait=1m")
	require.NoError(t, err, out)
	waitAllShutOff(t, names)
	// Whatever happens below, the testbed ends with its three nodes
	defer func() {
		if domstate(missing) != "running" {
			virsh(t, "start", missing)
		}
		powerHealthy(t, 20*time.Minute)
	}()
	for _, name := range returning {
		virsh(t, "start", name)
	}
	recovery := powerRecovery{t, time.Now()}
	recovery.nodesBack(booted, returning...)
	recovery.reached("the CronJob resumed without "+missing, func() bool { return cronSuspended(t, powerNamespace, "running") == "false" })
	recovery.reached("Argo CD's controller back without "+missing, func() bool { return controllerReplicas(t) == replicas })
	assert.False(t, markerGone(t), "noout came off while an OSD is away: Ceph should wait up to 30 minutes first")

	// noout's 30 minutes count from startup's first run, with a margin
	limit := powerRecovery{t, recovery.start.Add(40*time.Minute - powerLimit)}
	limit.reached("the marker gone", func() bool { return markerGone(t) })
	gone := time.Since(recovery.start)
	t.Logf("the marker lasted %s", gone.Round(time.Second))
	assert.GreaterOrEqual(t, gone, 29*time.Minute, "noout came off before its limit with an OSD away")
	noout, err := nooutSet(t)
	assert.NoError(t, err)
	assert.False(t, noout, "noout is still set")
}

// One measurement (docs/installation/testbed.md), not part of just testbed-power: CloudNativePG installed by hand on a stable layer 2
func TestPowerCutPostgres(t *testing.T) {
	testbedOnly(t)
	_, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl(""), "get", "crd", "clusters.postgresql.cnpg.io")
	if err != nil {
		t.Skip("CloudNativePG is not installed: this is a one-off measurement")
	}
	powerHealthy(t, 30*time.Minute)
	options := applyTestbedPostgres(t)
	status := func(field string) string {
		out, _ := k8s.RunKubectlAndGetOutputE(t, options, "get", "cluster", "db", "-o", "jsonpath={.status."+field+"}")
		return out
	}
	psql := func(pod, query string) (string, error) {
		out, err := k8s.RunKubectlAndGetOutputE(t, options, "exec", pod, "-c", "postgres", "--", "psql", "-d", "app", "-tAc", query)
		return strings.TrimSpace(out), err
	}
	require.Eventually(t, func() bool { return status("readyInstances") == "3" }, 30*time.Minute, 10*time.Second, "three instances are not ready")
	primary := status("currentPrimary")
	_, err = psql(primary, "CREATE TABLE probe (value text); INSERT INTO probe VALUES ('power-cut')")
	require.NoError(t, err)
	names, _ := testbedHosts(t)
	booted := bootIDs(t)

	cutEveryNode(t)
	start := time.Now()
	powerRecovery{t, start}.nodesBack(booted, names...)
	// Postgres itself tells: the Cluster's status still says what the operator saw before the cut
	require.Eventually(t, func() bool {
		value, err := psql(status("currentPrimary"), "SELECT value FROM probe")
		return err == nil && value == "power-cut"
	}, 45*time.Minute-time.Since(start), 15*time.Second, "no primary returned with the write")
	t.Logf("a primary has the write after %s", time.Since(start).Round(time.Second))
	require.Eventually(t, func() bool {
		replicas, err := psql(status("currentPrimary"), "SELECT count(*) FROM pg_stat_replication WHERE state = 'streaming'")
		return err == nil && replicas == "2"
	}, 45*time.Minute-time.Since(start), 15*time.Second, "the replicas did not return")
	t.Logf("two replicas streaming after %s", time.Since(start).Round(time.Second))
	t.Logf("primary %s before, %s after", primary, status("currentPrimary"))
}
