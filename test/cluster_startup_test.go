package test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A kubectl answering from files in $FAKE: data/<key> is the marker (no directory: NotFound), boot-<node> a node's boot ID,
// osds the OSD pods' readiness (no file: no Ceph), fail how many calls first find the API away, cron-gone and marker-gone
// a CronJob or marker another node removed meanwhile
const startupKubectl = `#!/usr/bin/env bash
echo "$*" >> "$FAKE/log"
n=$(cat "$FAKE/count" 2>/dev/null || echo 0)
echo $((n + 1)) > "$FAKE/count"
if [ "$n" -lt "$(cat "$FAKE/fail" 2>/dev/null || echo 0)" ]; then
	echo "The connection to the server was refused" >&2
	exit 1
fi
notfound() { echo "Error from server (NotFound): $1 not found" >&2; exit 1; }
case "$*" in
*"get configmap cluster-shutdown"*)
	[ -d "$FAKE/data" ] || notfound configmap
	key=$(echo "$*" | sed -n 's/.*{\.data\.\([a-zA-Z]*\)}.*/\1/p')
	cat "$FAKE/data/$key" 2>/dev/null || true
	;;
*"patch configmap"*) [ ! -f "$FAKE/marker-gone" ] || notfound configmap ;;
*"delete configmap"*) [ ! -f "$FAKE/marker-gone" ] || notfound configmap ;;
*"get node "*)
	node=$(echo "$*" | sed -n 's/.*get node \([^ ]*\).*/\1/p')
	cat "$FAKE/boot-$node"
	;;
*"patch cronjob"*) [ ! -f "$FAKE/cron-gone" ] || notfound cronjob ;;
*"get cephclusters"*)
	[ -f "$FAKE/osds" ] || { echo "error: the server doesn't have a resource type \"cephclusters\"" >&2; exit 1; }
	echo cephcluster.ceph.rook.io/rook-ceph
	;;
*"get pods"*) cat "$FAKE/osds" ;;
*"get clusters.postgresql"*) echo "error: the server doesn't have a resource type" >&2; exit 1 ;;
esac
`

type startupFake struct {
	t       *testing.T
	dir     string
	k3sYAML string
}

func newStartupFake(t *testing.T, marker map[string]string, boots map[string]string) *startupFake {
	t.Helper()
	f := &startupFake{t: t, dir: t.TempDir()}
	f.k3sYAML = filepath.Join(f.dir, "k3s.yaml")
	f.write("k3s.yaml", "")
	f.write("kubectl", startupKubectl)
	require.NoError(t, os.Chmod(filepath.Join(f.dir, "kubectl"), 0o755))
	if marker != nil {
		require.NoError(t, os.Mkdir(filepath.Join(f.dir, "data"), 0o755))
		for key, value := range marker {
			f.write("data/"+key, value)
		}
	}
	for node, id := range boots {
		f.write("boot-"+node, id)
	}
	return f
}

func (f *startupFake) write(name, body string) {
	f.t.Helper()
	require.NoError(f.t, os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o644))
}

func (f *startupFake) run(args ...string) (int, string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", append([]string{"../infra/_modules/metal/flake/profiles/cluster-startup.sh"}, args...)...)
	cmd.Env = append(os.Environ(), "FAKE="+f.dir, "KUBECTL="+filepath.Join(f.dir, "kubectl"), "K3S_YAML="+f.k3sYAML, "RETRY_SECONDS=0", "NOOUT_LIMIT=3600")
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		require.True(f.t, errors.As(err, &exit), "%v: %s", err, out)
		code = exit.ExitCode()
	}
	return code, string(out)
}

func (f *startupFake) log() []string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(f.t, err)
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// index is where the first logged call containing part sits, -1 when there is none
func (f *startupFake) index(part string) int {
	for i, line := range f.log() {
		if strings.Contains(line, part) {
			return i
		}
	}
	return -1
}

func shutdownMarker() map[string]string {
	return map[string]string{"replicas": "2", "cronjobs": "velero/snapshots\nopenbao/snapshot", "boots": "a=old-a\nb=old-b\nc=old-c"}
}

func newBoots() map[string]string { return map[string]string{"a": "new-a", "b": "new-b", "c": "new-c"} }

// A joiner not yet enrolled has no k3s to ask, and must not loop until it has
func TestClusterStartupNoK3s(t *testing.T) {
	f := newStartupFake(t, nil, nil)
	f.k3sYAML = filepath.Join(f.dir, "absent.yaml")
	code, out := f.run()
	assert.Equal(t, 0, code, out)
	assert.Empty(t, f.log())
}

func TestClusterStartupNoMarker(t *testing.T) {
	f := newStartupFake(t, nil, nil)
	code, out := f.run()
	assert.Equal(t, 0, code, out)
	assert.Equal(t, -1, f.index("patch"))
	assert.Equal(t, -1, f.index("delete"))
}

// An API away is not the absence of a marker: the other nodes may take any time to return
func TestClusterStartupAPIAway(t *testing.T) {
	f := newStartupFake(t, shutdownMarker(), newBoots())
	f.write("fail", "3")
	code, out := f.run()
	assert.Equal(t, 0, code, out)
	assert.NotEqual(t, -1, f.index("delete configmap cluster-shutdown"), "it stopped trying: %v", f.log())
}

func TestClusterStartupMinorityRebooted(t *testing.T) {
	f := newStartupFake(t, shutdownMarker(), map[string]string{"a": "new-a", "b": "old-b", "c": "old-c"})
	code, out := f.run()
	assert.Equal(t, 75, code, out)
	assert.Equal(t, -1, f.index("patch"))
	assert.Equal(t, -1, f.index("scale"))
	code, out = f.run("--force")
	assert.Equal(t, 0, code, out)
	assert.NotEqual(t, -1, f.index("delete configmap cluster-shutdown"))
}

// A node that never returns must not hold the rest back
func TestClusterStartupMajorityRebooted(t *testing.T) {
	f := newStartupFake(t, shutdownMarker(), map[string]string{"a": "new-a", "b": "new-b", "c": "old-c"})
	code, out := f.run()
	assert.Equal(t, 0, code, out)
	assert.NotEqual(t, -1, f.index("delete configmap cluster-shutdown"))
}

func TestClusterStartupUndoes(t *testing.T) {
	f := newStartupFake(t, shutdownMarker(), newBoots())
	f.write("osds", "True True True")
	code, out := f.run()
	assert.Equal(t, 0, code, out)
	assert.NotEqual(t, -1, f.index("-n velero patch cronjob snapshots"), "%v", f.log())
	assert.NotEqual(t, -1, f.index("-n openbao patch cronjob snapshot"), "%v", f.log())
	assert.NotEqual(t, -1, f.index("scale statefulset argocd-application-controller --replicas=2"), "%v", f.log())
	unset := f.index("osd unset noout")
	assert.NotEqual(t, -1, unset, "%v", f.log())
	log := f.log()
	assert.Contains(t, log[len(log)-1], "delete configmap cluster-shutdown", "the marker goes last")
	assert.Equal(t, 2, strings.Count(strings.Join(log, "\n"), "patch cronjob"), "only the marker's CronJobs")
}

func TestClusterStartupGoneMeanwhile(t *testing.T) {
	f := newStartupFake(t, shutdownMarker(), newBoots())
	f.write("cron-gone", "")
	f.write("marker-gone", "")
	code, out := f.run()
	assert.Equal(t, 0, code, out)
	assert.NotEqual(t, -1, f.index("scale statefulset"), "it did not finish: %v", f.log())
}

// Ceph short of an OSD for good: noout comes off so it copies what the dead node held, and the rest did not wait for that
func TestClusterStartupNoOutLimit(t *testing.T) {
	marker := shutdownMarker()
	marker["startupBegan"] = "1"
	f := newStartupFake(t, marker, newBoots())
	f.write("osds", "True False")
	code, out := f.run()
	assert.Equal(t, 0, code, out)
	unset := f.index("osd unset noout")
	require.NotEqual(t, -1, unset, "%v", f.log())
	assert.Less(t, f.index("patch cronjob"), unset)
	assert.Less(t, f.index("scale statefulset"), unset)
}

// The limit counts from the first start, whichever node or restart runs it now
func TestClusterStartupKeepsItsClock(t *testing.T) {
	marker := shutdownMarker()
	marker["startupBegan"] = "1"
	f := newStartupFake(t, marker, newBoots())
	code, out := f.run()
	assert.Equal(t, 0, code, out)
	assert.Equal(t, -1, f.index("patch configmap"), "%v", f.log())

	f = newStartupFake(t, shutdownMarker(), newBoots())
	code, out = f.run()
	assert.Equal(t, 0, code, out)
	assert.NotEqual(t, -1, f.index("patch configmap cluster-shutdown"), "%v", f.log())
}

func TestClusterStartupNoCeph(t *testing.T) {
	f := newStartupFake(t, shutdownMarker(), newBoots())
	code, out := f.run()
	assert.Equal(t, 0, code, out)
	assert.Equal(t, -1, f.index("osd unset noout"))
}
