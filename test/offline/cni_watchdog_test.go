package offline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKubectl answers the watchdog's queries from files and records deletions.
const fakeKubectl = `#!/bin/sh
printf '%s\n' "$*" >> "$FIXTURES/calls"
case "$*" in
  *"delete pod"*) printf '%s\n' "$*" >> "$FIXTURES/deleted" ;;
  *app=ztunnel*) cat "$FIXTURES/ztunnel" ;;
  *istio-cni-node*) node=$(echo "$*" | sed -n 's/.*spec.nodeName=\([^ ]*\).*/\1/p'); cat "$FIXTURES/cni-$node" 2>/dev/null ;;
  *FailedCreatePodSandBox*) cat "$FIXTURES/events" 2>/dev/null ;;
  *status.phase=Pending*) cat "$FIXTURES/pending" 2>/dev/null ;;
esac
`

// watchdogCluster is what the fake kubectl serves: ztunnel lines ("name node ready startedAt"), istio-cni lines per
// node ("name startedAt"), FailedCreatePodSandBox events ("namespace/name lastTimestamp message") and Pending pods
// ("namespace/name nodeName creationTimestamp PodReadyToStartContainers")
type watchdogCluster struct {
	ztunnel string
	cni     map[string]string
	events  string
	pending string
}

// runWatchdog runs system/istio-system/files/cni-watchdog.sh against the cluster and returns the kubectl deletions it
// made and the number of kubectl calls.
func runWatchdog(t *testing.T, c watchdogCluster) (string, int) {
	dir := t.TempDir()
	write := func(name, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeKubectl), 0o755))
	write("ztunnel", c.ztunnel)
	write("events", c.events)
	for node, lines := range c.cni {
		write("cni-"+node, lines)
	}
	write("pending", c.pending)
	cmd := exec.Command("sh", repo.Root()+"/system/istio-system/files/cni-watchdog.sh")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FIXTURES="+dir, "NOT_READY_SECONDS=180")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "watchdog failed:\n%s", out)
	deleted, _ := os.ReadFile(filepath.Join(dir, "deleted"))
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	return strings.TrimSpace(string(deleted)), strings.Count(string(calls), "\n")
}

func ago(seconds int) string {
	return time.Now().UTC().Add(-time.Duration(seconds) * time.Second).Format(time.RFC3339)
}

func TestIstioCNIWatchdog(t *testing.T) {
	cniA := map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n"}
	failed := `plugin type="istio-cni" name="istio-cni" failed (add): istio-cni cmdAdd failed to contact node Istio CNI agent`
	stuck := "default/web node-a " + ago(400) + " False\n"
	deleteCNI := "-n istio-system delete pod istio-cni-a --wait=false"
	for _, c := range []struct {
		name    string
		cluster watchdogCluster
		deleted string
	}{
		{"not ready after istio-cni's restart: both", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a false %s\n", ago(600)), cni: cniA}, "-n istio-system delete pod istio-cni-a ztunnel-a --wait=false"},
		{"stuck node", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a false %s\n", ago(300)), cni: cniA}, deleteCNI},
		{"ztunnel just started", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a false %s\n", ago(60)), cni: cniA}, ""},
		{"ztunnel ready", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(300)), cni: cniA}, ""},
		{"istio-cni just restarted", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a false %s\n", ago(300)), cni: map[string]string{"node-a": "istio-cni-a " + ago(60) + "\n"}}, ""},
		{"ztunnel not running", watchdogCluster{ztunnel: "ztunnel-a node-a false \n", cni: cniA}, ""},
		{"no istio-cni pod", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a false %s\nztunnel-b node-b false %s\n", ago(300), ago(300)), cni: map[string]string{"node-b": "istio-cni-b " + ago(300) + "\n"}}, "-n istio-system delete pod istio-cni-b --wait=false"},
		{"unparsable start time", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a false garbage\nztunnel-b node-b false %s\n", ago(300)), cni: map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n", "node-b": "istio-cni-b " + ago(300) + "\n"}}, "-n istio-system delete pod istio-cni-b --wait=false"},
		{"only the stuck node", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\nztunnel-b node-b false %s\n", ago(300), ago(300)), cni: map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n", "node-b": "istio-cni-b " + ago(300) + "\n"}}, "-n istio-system delete pod istio-cni-b --wait=false"},
		// ztunnel Ready while pods cannot get their sandbox (12a-3, 15f)
		{"stuck pods, ztunnel ready", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: map[string]string{"node-a": "istio-cni-a " + ago(500) + "\n"}, events: "default/web " + ago(30) + " " + failed + "\n", pending: stuck}, deleteCNI},
		{"stuck pods, istio-cni restarted after ztunnel: both", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(600)), cni: cniA, events: "default/web " + ago(30) + " " + failed + "\n", pending: stuck}, "-n istio-system delete pod istio-cni-a ztunnel-a --wait=false"},
		{"stuck pods, ztunnel just restarted", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(60)), cni: cniA, events: "default/web " + ago(30) + " " + failed + "\n", pending: stuck}, ""},
		{"old event", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "default/web " + ago(600) + " " + failed + "\n", pending: stuck}, ""},
		{"another plugin", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "default/web " + ago(30) + ` plugin type="flannel" name="cbr0" failed (add): no IP addresses available` + "\n", pending: stuck}, ""},
		{"pod running", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "default/web " + ago(30) + " " + failed + "\n", pending: ""}, ""},
		{"young pod", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "default/web " + ago(30) + " " + failed + "\n", pending: "default/web node-a " + ago(60) + " False\n"}, ""},
		{"stuck pods on another node", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "default/web " + ago(30) + " " + failed + "\n", pending: "default/web node-b " + ago(400) + " False\n"}, ""},
		// Its sandbox exists: Pending for an init container or an image pull, after one CNI failure in the window
		{"sandbox ready, still pending", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "default/web " + ago(30) + " " + failed + "\n", pending: "default/web node-a " + ago(400) + " True\n"}, ""},
		{"same name, another namespace", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "other/web " + ago(30) + " " + failed + "\n", pending: stuck}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			deleted, _ := runWatchdog(t, c.cluster)
			assert.Equal(t, c.deleted, deleted)
		})
	}
}

// After a power cut, hundreds of sandbox events: the watchdog asks the API a fixed number of times, so its Job ends
// well within its 50 s deadline
func TestIstioCNIWatchdogFewCalls(t *testing.T) {
	failed := `plugin type="istio-cni" name="istio-cni" failed (add): istio-cni cmdAdd failed to contact node Istio CNI agent`
	var events, pending strings.Builder
	for i := 0; i < 50; i++ {
		for j := 0; j < 5; j++ {
			fmt.Fprintf(&events, "default/web-%d %s %s\n", i, ago(30+j), failed)
		}
		fmt.Fprintf(&pending, "default/web-%d node-a %s False\n", i, ago(400))
	}
	deleted, calls := runWatchdog(t, watchdogCluster{
		ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)),
		cni:     map[string]string{"node-a": "istio-cni-a " + ago(500) + "\n"},
		events:  events.String(), pending: pending.String(),
	})
	assert.Equal(t, "-n istio-system delete pod istio-cni-a --wait=false", deleted)
	assert.LessOrEqual(t, calls, 5, "one call per query, not per event or pod")
}

// Pods only get IPv4 (cni.ambient.ipv6 false), so ztunnel binds its in-pod DNS proxy on 127.0.0.1 only: it decides
// IPv6 once, in its own network namespace, and then bound [::1]:15053 in every pod's, which a sandbox whose loopback is
// not up yet, or is being torn down, does not have. After a power cut those binds failed for every pod, their retries
// made each add miss istio-cni's 5 s, the sandboxes were torn down and retried in turn, and the sandbox never recovered
// (2026-11-13); IPV6_ENABLED=false recovered it within a minute
func TestZtunnelIPv4Only(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		assert.Equal(t, "false", renderVelero(t, "system/istio-system", env,
			`select(.kind == "DaemonSet" and .metadata.name == "ztunnel") | .spec.template.spec.containers[0].env[] | select(.name == "IPV6_ENABLED") | .value`), env)
		assert.Equal(t, "false", renderVelero(t, "system/istio-system", env,
			`select(.kind == "ConfigMap" and .metadata.name == "istio-cni-config") | .data.AMBIENT_IPV6`), env, "the two must agree")
	}
}
