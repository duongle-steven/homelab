package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKubectl answers the watchdog's queries from files and records deletions.
const fakeKubectl = `#!/bin/sh
case "$*" in
  *"delete pod"*) echo "$*" >> "$FIXTURES/deleted" ;;
  *app=ztunnel*) cat "$FIXTURES/ztunnel" ;;
  *istio-cni-node*) node=$(echo "$*" | sed -n 's/.*spec.nodeName=\([^ ]*\).*/\1/p'); cat "$FIXTURES/cni-$node" 2>/dev/null ;;
  *FailedCreatePodSandBox*) cat "$FIXTURES/events" 2>/dev/null ;;
  *"get pod "*) pod=$(echo "$*" | sed -n 's/.*get pod \([^ ]*\).*/\1/p'); cat "$FIXTURES/pod-$pod" 2>/dev/null ;;
esac
`

// watchdogCluster is what the fake kubectl serves: ztunnel lines ("name node ready startedAt"), istio-cni lines per
// node ("name startedAt"), FailedCreatePodSandBox events ("namespace/name lastTimestamp message") and pods by name
// ("nodeName phase creationTimestamp")
type watchdogCluster struct {
	ztunnel string
	cni     map[string]string
	events  string
	pods    map[string]string
}

// runWatchdog runs system/istio-system/files/cni-watchdog.sh against the cluster and returns the kubectl deletions it made.
func runWatchdog(t *testing.T, c watchdogCluster) string {
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
	for pod, line := range c.pods {
		write("pod-"+pod, line)
	}
	cmd := exec.Command("sh", "../system/istio-system/files/cni-watchdog.sh")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FIXTURES="+dir, "NOT_READY_SECONDS=180")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "watchdog failed:\n%s", out)
	deleted, _ := os.ReadFile(filepath.Join(dir, "deleted"))
	return strings.TrimSpace(string(deleted))
}

func ago(seconds int) string {
	return time.Now().UTC().Add(-time.Duration(seconds) * time.Second).Format(time.RFC3339)
}

func TestIstioCNIWatchdog(t *testing.T) {
	cniA := map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n"}
	failed := `plugin type="istio-cni" name="istio-cni" failed (add): istio-cni cmdAdd failed to contact node Istio CNI agent`
	stuck := map[string]string{"web": "node-a Pending " + ago(400)}
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
		{"stuck pods, ztunnel ready", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: map[string]string{"node-a": "istio-cni-a " + ago(500) + "\n"}, events: "default/web " + ago(30) + " " + failed + "\n", pods: stuck}, deleteCNI},
		{"stuck pods, istio-cni restarted after ztunnel: both", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(600)), cni: cniA, events: "default/web " + ago(30) + " " + failed + "\n", pods: stuck}, "-n istio-system delete pod istio-cni-a ztunnel-a --wait=false"},
		{"stuck pods, ztunnel just restarted", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(60)), cni: cniA, events: "default/web " + ago(30) + " " + failed + "\n", pods: stuck}, ""},
		{"old event", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "default/web " + ago(600) + " " + failed + "\n", pods: stuck}, ""},
		{"another plugin", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "default/web " + ago(30) + ` plugin type="flannel" name="cbr0" failed (add): no IP addresses available` + "\n", pods: stuck}, ""},
		{"pod running", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "default/web " + ago(30) + " " + failed + "\n", pods: map[string]string{"web": "node-a Running " + ago(400)}}, ""},
		{"young pod", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "default/web " + ago(30) + " " + failed + "\n", pods: map[string]string{"web": "node-a Pending " + ago(60)}}, ""},
		{"stuck pods on another node", watchdogCluster{ztunnel: fmt.Sprintf("ztunnel-a node-a true %s\n", ago(400)), cni: cniA, events: "default/web " + ago(30) + " " + failed + "\n", pods: map[string]string{"web": "node-b Pending " + ago(400)}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.deleted, runWatchdog(t, c.cluster))
		})
	}
}
