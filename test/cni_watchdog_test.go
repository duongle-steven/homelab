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

// fakeKubectl answers the watchdog's two queries from files and records deletions.
const fakeKubectl = `#!/bin/sh
case "$*" in
  *"delete pod"*) echo "$*" >> "$FIXTURES/deleted" ;;
  *app=ztunnel*) cat "$FIXTURES/ztunnel" ;;
  *istio-cni-node*) node=$(echo "$*" | sed -n 's/.*spec.nodeName=\([^ ]*\).*/\1/p'); cat "$FIXTURES/cni-$node" 2>/dev/null ;;
esac
`

// runWatchdog runs system/istio-system/files/cni-watchdog.sh against ztunnel lines ("node ready startedAt") and
// istio-cni lines per node ("pod startedAt"), and returns the kubectl deletions it made.
func runWatchdog(t *testing.T, ztunnel string, cni map[string]string) string {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeKubectl), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ztunnel"), []byte(ztunnel), 0o644))
	for node, lines := range cni {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "cni-"+node), []byte(lines), 0o644))
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
	for _, c := range []struct {
		name    string
		ztunnel string
		cni     map[string]string
		deleted string
	}{
		{"stuck node", fmt.Sprintf("node-a false %s\n", ago(300)), map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n"}, "-n istio-system delete pod istio-cni-a --wait=false"},
		{"ztunnel just started", fmt.Sprintf("node-a false %s\n", ago(60)), map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n"}, ""},
		{"ztunnel ready", fmt.Sprintf("node-a true %s\n", ago(300)), map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n"}, ""},
		{"istio-cni just restarted", fmt.Sprintf("node-a false %s\n", ago(300)), map[string]string{"node-a": "istio-cni-a " + ago(60) + "\n"}, ""},
		{"ztunnel not running", "node-a false \n", map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n"}, ""},
		{"no istio-cni pod", fmt.Sprintf("node-a false %s\nnode-b false %s\n", ago(300), ago(300)), map[string]string{"node-b": "istio-cni-b " + ago(300) + "\n"}, "-n istio-system delete pod istio-cni-b --wait=false"},
		{"unparsable start time", fmt.Sprintf("node-a false garbage\nnode-b false %s\n", ago(300)), map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n", "node-b": "istio-cni-b " + ago(300) + "\n"}, "-n istio-system delete pod istio-cni-b --wait=false"},
		{"only the stuck node", fmt.Sprintf("node-a true %s\nnode-b false %s\n", ago(300), ago(300)), map[string]string{"node-a": "istio-cni-a " + ago(300) + "\n", "node-b": "istio-cni-b " + ago(300) + "\n"}, "-n istio-system delete pod istio-cni-b --wait=false"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.deleted, runWatchdog(t, c.ztunnel, c.cni))
		})
	}
}
