package test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Testbed's tests run only against testbed (make testbed: ENV=testbed), its three NixOS VMs
func testbedOnly(t *testing.T) {
	if os.Getenv("ENV") != "testbed" {
		t.Skip("testbed tests run against testbed only (make testbed)")
	}
}

// testbedHosts reads the VMs' names and addresses (infra/testbed/metal/hosts.json), sorted by name
func testbedHosts(t *testing.T) (names []string, ips map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "infra", "testbed", "metal", "hosts.json"))
	require.NoError(t, err)
	var hosts map[string]struct {
		IP string `json:"ip"`
	}
	require.NoError(t, json.Unmarshal(raw, &hosts))
	ips = map[string]string{}
	for name, host := range hosts {
		names = append(names, name)
		ips[name] = host.IP
	}
	sort.Strings(names)
	return names, ips
}

func testbedSSH(t *testing.T, ip, command string) (string, error) {
	t.Helper()
	out, err := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "root@"+ip, command).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func virsh(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("virsh", append([]string{"-c", "qemu:///system"}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

// domstate reports a VM's state without failing the test: it runs inside Eventually's condition
func domstate(name string) string {
	out, _ := exec.Command("virsh", "-c", "qemu:///system", "domstate", name).Output()
	return strings.TrimSpace(string(out))
}

func testbedKubectl(namespace string) *k8s.KubectlOptions {
	options := k8s.NewKubectlOptions("", "", namespace)
	options.Logger = logger.Discard
	return options
}

// waitNodeReady waits until node reports Ready
func waitNodeReady(t *testing.T, node string, wait time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl(""), "get", "node", node,
			"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
		return err == nil && out == "True"
	}, wait, 10*time.Second, "%s is not Ready", node)
}

// Before the other tests read the running result: every Application the list names has synced this commit and is healthy.
// Without this a change Argo CD has not reached yet, or a sync that failed, would be marked as proved
func TestTestbedSynced(t *testing.T) {
	testbedOnly(t)
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	commit := strings.TrimSpace(string(head))
	listed, err := exec.Command("yq", `.argocd-apps.applicationsets.root.generators[0].matrix.generators[0].git.directories[].path`, "../system/argocd/values-testbed.yaml").Output()
	require.NoError(t, err)
	var want []string
	for _, path := range strings.Fields(string(listed)) {
		want = append(want, filepath.Base(path))
	}
	require.NotEmpty(t, want)
	var waiting []string
	if !assert.Eventually(t, func() bool {
		waiting = nil
		for _, name := range want {
			out, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl("argocd"), "get", "application", name, "-o", "json")
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
			if err != nil || json.Unmarshal([]byte(out), &app) != nil {
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
	}, 35*time.Minute, 15*time.Second) {
		t.Logf("still waiting: %v", waiting)
	}
}

// Three k3s servers, each an etcd member that sees a leader
func TestTestbedNodes(t *testing.T) {
	testbedOnly(t)
	names, ips := testbedHosts(t)
	require.Len(t, names, 3)
	for _, name := range names {
		waitNodeReady(t, name, 5*time.Minute)
		roles, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl(""), "get", "node", name, "-o", "jsonpath={.metadata.labels}")
		require.NoError(t, err)
		assert.Contains(t, roles, `"node-role.kubernetes.io/control-plane":"true"`, name)
		assert.Contains(t, roles, `"node-role.kubernetes.io/etcd":"true"`, name)
		metrics, err := testbedSSH(t, ips[name], "curl -s http://127.0.0.1:2381/metrics | grep '^etcd_server_has_leader '")
		require.NoError(t, err, metrics)
		assert.Equal(t, "etcd_server_has_leader 1", metrics, name)
	}
}

// The API's VIP moves: with the node holding it shut down, the API still answers through the VIP from another node
func TestTestbedVIPMoves(t *testing.T) {
	testbedOnly(t)
	holder, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl("kube-system"), "get", "lease", "plndr-cp-lock",
		"-o", "jsonpath={.spec.holderIdentity}")
	require.NoError(t, err)
	require.NotEmpty(t, holder, "no kube-vip leader for the control plane")
	bootID := func() string {
		id, _ := k8s.RunKubectlAndGetOutputE(t, testbedKubectl(""), "get", "node", holder, "-o", "jsonpath={.status.nodeInfo.bootID}")
		return id
	}
	booted := bootID()
	t.Logf("%s holds the VIP: shutting it down", holder)
	virsh(t, "shutdown", holder)
	defer func() {
		require.Eventually(t, func() bool { return domstate(holder) == "shut off" }, 3*time.Minute, 5*time.Second)
		virsh(t, "start", holder)
		// The API server reports the node's last status until its kubelet reports again: wait for the new boot
		require.Eventually(t, func() bool { id := bootID(); return id != "" && id != booted }, 10*time.Minute, 10*time.Second,
			"%s did not come back", holder)
		waitNodeReady(t, holder, 10*time.Minute)
	}()
	require.Eventually(t, func() bool { return domstate(holder) == "shut off" }, 3*time.Minute, 5*time.Second,
		"%s did not shut down", holder)
	start := time.Now()
	assert.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl(""), "get", "--raw", "/readyz")
		return err == nil && out == "ok"
	}, 2*time.Minute, 5*time.Second, "the API did not answer through the VIP without %s", holder)
	t.Logf("the API answered through the VIP %s after %s's shutdown", time.Since(start).Round(time.Second), holder)
	next, err := k8s.RunKubectlAndGetOutputE(t, testbedKubectl("kube-system"), "get", "lease", "plndr-cp-lock",
		"-o", "jsonpath={.spec.holderIdentity}")
	require.NoError(t, err)
	assert.NotEqual(t, holder, next, "the VIP did not move")
}

// Ceph on three nodes: healthy, an OSD on each node, and a volume written on one node read back from another
func TestTestbedCeph(t *testing.T) {
	testbedOnly(t)
	ceph := testbedKubectl("rook-ceph")
	require.Eventually(t, func() bool {
		health, err := k8s.RunKubectlAndGetOutputE(t, ceph, "get", "cephcluster", "-o", "jsonpath={.items[*].status.ceph.health}")
		return err == nil && health == "HEALTH_OK"
	}, 30*time.Minute, 15*time.Second, "Ceph is not HEALTH_OK")
	names, _ := testbedHosts(t)
	// HEALTH_OK can come while Rook still recreates an OSD's pod
	var osds string
	if !assert.Eventually(t, func() bool {
		osds, _ = k8s.RunKubectlAndGetOutputE(t, ceph, "get", "pods", "-l", "app=rook-ceph-osd",
			"-o", `jsonpath={range .items[*]}{.spec.nodeName}{" "}{.status.phase}{"\n"}{end}`)
		for _, name := range names {
			if !strings.Contains(osds, name+" Running") {
				return false
			}
		}
		return true
	}, 10*time.Minute, 15*time.Second) {
		t.Logf("an OSD is not running:\n%s", osds)
	}

	ns := fmt.Sprintf("testbed-ceph-%d", time.Now().Unix())
	options := testbedKubectl(ns)
	k8s.CreateNamespace(t, options, ns)
	defer k8s.DeleteNamespace(t, options, ns)
	pod := func(name, node, command string) string {
		return fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":%q},"spec":{"nodeName":%q,"restartPolicy":"Never",
			"containers":[{"name":"c","image":"busybox:1.37.0","command":["sh","-c",%q],"volumeMounts":[{"name":"v","mountPath":"/data"}]}],
			"volumes":[{"name":"v","persistentVolumeClaim":{"claimName":"data"}}]}}`, name, node, command)
	}
	claim := `{"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"name":"data"},
		"spec":{"accessModes":["ReadWriteOnce"],"storageClassName":"standard-rwo","resources":{"requests":{"storage":"1Gi"}}}}`
	k8s.KubectlApplyFromString(t, options, claim)
	k8s.KubectlApplyFromString(t, options, pod("writer", names[0], "echo testbed-ceph > /data/probe && sync"))
	waitPodSucceeded(t, options, "writer")
	k8s.RunKubectl(t, options, "delete", "pod", "writer", "--wait")
	k8s.KubectlApplyFromString(t, options, pod("reader", names[1], "cat /data/probe"))
	waitPodSucceeded(t, options, "reader")
	logs, err := k8s.RunKubectlAndGetOutputE(t, options, "logs", "reader")
	require.NoError(t, err)
	assert.Equal(t, "testbed-ceph", logs, "written on %s, read on %s", names[0], names[1])
}

func waitPodSucceeded(t *testing.T, options *k8s.KubectlOptions, name string) {
	t.Helper()
	var phase string
	require.Eventually(t, func() bool {
		phase, _ = k8s.RunKubectlAndGetOutputE(t, options, "get", "pod", name, "-o", "jsonpath={.status.phase}")
		return phase == "Succeeded" || phase == "Failed"
	}, 5*time.Minute, 5*time.Second, "pod %s did not finish", name)
	require.Equal(t, "Succeeded", phase, "pod %s failed", name)
}

// Layer 2: pods on two nodes reach each other through ztunnel, the mesh refuses what no policy allows, and the Gateway
// answers at its VIP with a certificate from the local CA
func TestTestbedMesh(t *testing.T) {
	testbedOnly(t)
	system := testbedKubectl("istio-system")
	require.Eventually(t, func() bool {
		out, err := k8s.RunKubectlAndGetOutputE(t, system, "get", "gateway", "gateway",
			"-o", `jsonpath={.status.conditions[?(@.type=="Programmed")].status}`)
		return err == nil && out == "True"
	}, 30*time.Minute, 15*time.Second, "the Gateway is not programmed")

	names, _ := testbedHosts(t)
	suffix := time.Now().Unix()
	meshed := fmt.Sprintf("testbed-mesh-%d", suffix)
	stranger := fmt.Sprintf("testbed-stranger-%d", suffix)
	outside := fmt.Sprintf("testbed-outside-%d", suffix)
	options := testbedKubectl(meshed)
	namespace := func(name, mode string) {
		k8s.KubectlApplyFromString(t, options, fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q,"labels":{"istio.io/dataplane-mode":%q}}}`, name, mode))
	}
	for name, mode := range map[string]string{meshed: "ambient", stranger: "ambient", outside: "none"} {
		namespace(name, mode)
		defer k8s.DeleteNamespace(t, testbedKubectl(name), name)
	}
	k8s.KubectlApplyFromString(t, options, fmt.Sprintf(`{"apiVersion":"security.istio.io/v1","kind":"AuthorizationPolicy","metadata":{"name":"allow","namespace":%q},
		"spec":{"action":"ALLOW","rules":[{"from":[{"source":{"namespaces":[%q]}}]}]}}`, meshed, meshed))
	k8s.KubectlApplyFromString(t, options, fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"server"},"spec":{"nodeName":%q,
		"containers":[{"name":"c","image":"busybox:1.37.0","command":["sh","-c","mkdir -p /www && echo ok > /www/index.html && exec httpd -f -p 8080 -h /www"]}]}}`, names[0]))
	k8s.WaitUntilPodAvailable(t, options, "server", 40, 15*time.Second)
	redirection, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "pod", "server", "-o", `jsonpath={.metadata.annotations.ambient\.istio\.io/redirection}`)
	require.NoError(t, err)
	assert.Equal(t, "enabled", redirection, "the server is not redirected to ztunnel")
	ip, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "pod", "server", "-o", "jsonpath={.status.podIP}")
	require.NoError(t, err)

	// From another node, so the connection crosses between two ztunnels
	curl := func(namespace, name string) string {
		client := testbedKubectl(namespace)
		k8s.KubectlApplyFromString(t, client, fmt.Sprintf(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":%q},"spec":{"nodeName":%q,"restartPolicy":"Never",
			"containers":[{"name":"c","image":"curlimages/curl:8.23.0","command":["sh","-c",%q]}]}}`, name, names[1],
			fmt.Sprintf(`sleep 5; curl -s -m 10 -o /dev/null -w "%%{http_code}" http://%s:8080/ || true`, ip)))
		waitPodSucceeded(t, client, name)
		out, err := k8s.RunKubectlAndGetOutputE(t, client, "logs", name)
		require.NoError(t, err)
		return strings.TrimSpace(out)
	}
	since := time.Now().UTC().Format(time.RFC3339)
	assert.Equal(t, "200", curl(meshed, "neighbour"), "a meshed pod on another node, in the allowed namespace")
	assert.Equal(t, "000", curl(stranger, "stranger"), "a meshed pod no policy allows")
	assert.Equal(t, "000", curl(outside, "plaintext"), "a pod outside the mesh, in plaintext")
	logs, err := k8s.RunKubectlAndGetOutputE(t, system, "logs", "--selector=app=ztunnel", "--prefix", "--since-time="+since, "--tail=-1")
	require.NoError(t, err)
	source := func(namespace string) string {
		return fmt.Sprintf(`src.identity="spiffe://cluster.local/ns/%s/sa/default"`, namespace)
	}
	assert.Contains(t, logs, source(meshed), "ztunnel did not carry the neighbour's identity")
	assert.Regexp(t, regexp.QuoteMeta(source(stranger))+`[^\n]*policy rejection|policy rejection[^\n]*`+regexp.QuoteMeta(source(stranger)), logs, "ztunnel did not refuse the stranger by policy")

	vip, err := testbedGatewayVIP()
	require.NoError(t, err)
	host := "probe." + testDomain(t)
	var answer, issuer []byte
	if !assert.Eventually(t, func() bool {
		answer, err = exec.Command("curl", "-sk", "-m", "10", "-o", "/dev/null", "-w", "%{http_code}", "--resolve", host+":80:"+vip, "http://"+host+"/").CombinedOutput()
		return err == nil && string(answer) == "301"
	}, 10*time.Minute, 10*time.Second) {
		t.Logf("port 80 at the VIP: %s", answer)
	}
	if !assert.Eventually(t, func() bool {
		issuer, err = exec.Command("sh", "-c", fmt.Sprintf(`curl -skv -m 10 -o /dev/null --resolve %s:443:%s https://%s/ 2>&1 | grep -i 'issuer:'`, host, vip, host)).CombinedOutput()
		return err == nil && strings.Contains(string(issuer), "homelab-dev-ca")
	}, 10*time.Minute, 10*time.Second) {
		t.Logf("the certificate at the VIP: %s", issuer)
	}
}

// Three CloudNativePG instances across the nodes, in the mesh and on Ceph as Forgejo's database is in production
// (platform/forgejo/templates/database.yaml): the primary's pod goes, a replica on another node takes over and keeps the data.
// This workstation does not hold that layer (docs/installation/testbed.md): it runs where the list names the operator
func TestTestbedPostgresFailover(t *testing.T) {
	testbedOnly(t)
	listed, err := exec.Command("yq", `.argocd-apps.applicationsets.root.generators[0].matrix.generators[0].git.directories[].path`, "../system/argocd/values-testbed.yaml").Output()
	require.NoError(t, err)
	if !strings.Contains(string(listed), "platform/cloudnative-pg") {
		t.Skip("system/argocd/values-testbed.yaml does not list platform/cloudnative-pg")
	}
	out, err := exec.Command("yq", ".database.cnpg.image", "../platform/forgejo/values.yaml").Output()
	require.NoError(t, err)
	image := strings.TrimSpace(string(out))
	ns := fmt.Sprintf("testbed-postgres-%d", time.Now().Unix())
	options := testbedKubectl(ns)
	k8s.KubectlApplyFromString(t, options, fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q,"labels":{"istio.io/dataplane-mode":"ambient"}}}`, ns))
	defer k8s.DeleteNamespace(t, options, ns)
	k8s.KubectlApplyFromString(t, options, fmt.Sprintf(`{"apiVersion":"v1","kind":"List","items":[
		{"apiVersion":"security.istio.io/v1","kind":"AuthorizationPolicy","metadata":{"name":"allow","namespace":%[1]q},
			"spec":{"action":"ALLOW","rules":[{"from":[{"source":{"namespaces":[%[1]q]}}]}]}},
		{"apiVersion":"security.istio.io/v1","kind":"AuthorizationPolicy","metadata":{"name":"allow-cnpg-status","namespace":%[1]q},
			"spec":{"action":"ALLOW","selector":{"matchLabels":{"cnpg.io/cluster":"db"}},"rules":[{"to":[{"operation":{"ports":["8000"]}}]}]}},
		{"apiVersion":"security.istio.io/v1","kind":"PeerAuthentication","metadata":{"name":"db-status","namespace":%[1]q},
			"spec":{"selector":{"matchLabels":{"cnpg.io/cluster":"db"}},"mtls":{"mode":"STRICT"},"portLevelMtls":{"8000":{"mode":"PERMISSIVE"}}}}]}`, ns))
	cluster := fmt.Sprintf(`{"apiVersion":"postgresql.cnpg.io/v1","kind":"Cluster","metadata":{"name":"db","namespace":%q},
		"spec":{"instances":3,"imageName":%q,"storage":{"size":"1Gi","storageClass":"standard-rwo"}}}`, ns, image)
	// The operator's CRD and webhook may still be coming up on a fresh install
	require.Eventually(t, func() bool {
		return k8s.KubectlApplyFromStringE(t, options, cluster) == nil
	}, 20*time.Minute, 15*time.Second, "CloudNativePG refuses the Cluster")

	status := func(field string) string {
		out, _ := k8s.RunKubectlAndGetOutputE(t, options, "get", "cluster", "db", "-o", "jsonpath={.status."+field+"}")
		return out
	}
	allReady := func(message string) {
		require.Eventually(t, func() bool { return status("readyInstances") == "3" }, 30*time.Minute, 10*time.Second, message)
	}
	psql := func(pod, query string) (string, error) {
		out, err := k8s.RunKubectlAndGetOutputE(t, options, "exec", pod, "-c", "postgres", "--", "psql", "-d", "app", "-tAc", query)
		return strings.TrimSpace(out), err
	}
	node := func(pod string) string {
		out, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "pod", pod, "-o", "jsonpath={.spec.nodeName}")
		require.NoError(t, err)
		return out
	}
	allReady("three instances are not ready")
	primary := status("currentPrimary")
	require.NotEmpty(t, primary)
	_, err = psql(primary, "CREATE TABLE probe (value text); INSERT INTO probe VALUES ('testbed-postgres')")
	require.NoError(t, err)
	pods, err := k8s.RunKubectlAndGetOutputE(t, options, "get", "pods", "-l", "cnpg.io/cluster=db,cnpg.io/instanceRole=replica", "-o", "jsonpath={.items[*].metadata.name}")
	require.NoError(t, err)
	replicas := strings.Fields(pods)
	require.Len(t, replicas, 2)
	// Replication is asynchronous: only a row the replicas hold must survive
	for _, replica := range replicas {
		require.Eventually(t, func() bool {
			value, err := psql(replica, "SELECT value FROM probe")
			return err == nil && value == "testbed-postgres"
		}, 2*time.Minute, 5*time.Second, "%s did not receive the row", replica)
	}

	lost := node(primary)
	k8s.RunKubectl(t, options, "delete", "pod", primary, "--wait=false")
	require.Eventually(t, func() bool {
		current := status("currentPrimary")
		return current != "" && current != primary && status("targetPrimary") == current
	}, 5*time.Minute, 5*time.Second, "no replica was promoted")
	promoted := status("currentPrimary")
	assert.NotEqual(t, lost, node(promoted), "the new primary is on the node that lost the old one")
	require.Eventually(t, func() bool {
		value, err := psql(promoted, "INSERT INTO probe VALUES ('after'); SELECT value FROM probe WHERE value = 'testbed-postgres'")
		return err == nil && strings.HasSuffix(value, "testbed-postgres")
	}, 3*time.Minute, 5*time.Second, "the promoted %s neither keeps the row nor takes writes", promoted)
	// The old primary comes back as a replica of the new one
	allReady("the old primary did not come back")
	require.Eventually(t, func() bool {
		value, err := psql(primary, "SELECT count(*) FROM probe")
		return err == nil && value == "2"
	}, 5*time.Minute, 5*time.Second, "%s did not catch up", primary)
}
