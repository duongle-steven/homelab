package cluster

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

type fakeUpgrader struct {
	current   map[string]bool
	unhealthy string // healthy fails after this node's upgrade
	sick      bool   // healthy always fails: the cluster is degraded already
	calls     []string
}

func (f *fakeUpgrader) record(call string) { f.calls = append(f.calls, call) }

func (f *fakeUpgrader) isCurrent(_ context.Context, node host) (bool, error) {
	f.record("current " + node.name)
	return f.current[node.name], nil
}
func (f *fakeUpgrader) drain(_ context.Context, node host) error {
	f.record("drain " + node.name)
	return nil
}
func (f *fakeUpgrader) rebuild(_ context.Context, node host) error {
	f.record("rebuild " + node.name)
	return nil
}
func (f *fakeUpgrader) ready(_ context.Context, node host) error {
	f.record("ready " + node.name)
	return nil
}
func (f *fakeUpgrader) healthy(context.Context) error {
	last := f.calls[len(f.calls)-1]
	f.record("healthy")
	if f.sick || (f.unhealthy != "" && last == "uncordon "+f.unhealthy) {
		return errors.New("Ceph HEALTH_WARN")
	}
	return nil
}
func (f *fakeUpgrader) uncordon(_ context.Context, node host) error {
	f.record("uncordon " + node.name)
	return nil
}

func upgradeFixture() config {
	node := func(name, ip string) host { return host{name, netip.MustParseAddrPort(ip + ":22")} }
	return config{seed: node("node-a", "192.0.2.11"), joiners: []host{node("node-b", "192.0.2.12"), node("node-c", "192.0.2.13")}}
}

// A node already running its flake's system is left alone: no drain, no rebuild
func TestUpgradeSkipsCurrentNodes(t *testing.T) {
	f := &fakeUpgrader{current: map[string]bool{"node-a": true, "node-b": true, "node-c": true}}
	if err := upgrade(t.Context(), upgradeFixture(), f, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls {
		if !strings.HasPrefix(call, "current ") {
			t.Fatalf("touched a current node: %v", f.calls)
		}
	}
}

// One node at a time, the cluster's initializer first: the cluster healthy before each is drained, rebuilt, back Ready and uncordoned, and the
// cluster healthy again before the next is touched (the node's own Ceph mon and OSD come back only once it is
// uncordoned)
func TestUpgradeOneAtATime(t *testing.T) {
	f := &fakeUpgrader{current: map[string]bool{"node-b": true}}
	if err := upgrade(t.Context(), upgradeFixture(), f, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"current node-a", "healthy", "drain node-a", "rebuild node-a", "ready node-a", "uncordon node-a", "healthy",
		"current node-b",
		"current node-c", "healthy", "drain node-c", "rebuild node-c", "ready node-c", "uncordon node-c", "healthy",
	}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("calls\n got %v\nwant %v", f.calls, want)
	}
}

// An unhealthy cluster after a node's rebuild stops the upgrade there: the next is untouched, and the error names the
// node
func TestUpgradeStopsOnUnhealthy(t *testing.T) {
	f := &fakeUpgrader{unhealthy: "node-a"}
	err := upgrade(t.Context(), upgradeFixture(), f, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "node-a") {
		t.Fatalf("error %v, want one naming node-a", err)
	}
	for _, call := range f.calls {
		if strings.HasSuffix(call, "node-b") {
			t.Fatalf("went on after an unhealthy cluster: %v", f.calls)
		}
	}
}

// A cluster already unhealthy (a failed run left a node down, say) is never made worse: the next node that needs an
// upgrade is not drained
func TestUpgradeWaitsForHealthBeforeDraining(t *testing.T) {
	f := &fakeUpgrader{current: map[string]bool{"node-a": true}, sick: true}
	if err := upgrade(t.Context(), upgradeFixture(), f, io.Discard); err == nil {
		t.Fatal("upgraded a degraded cluster")
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "drain ") {
			t.Fatalf("drained a node of a degraded cluster: %v", f.calls)
		}
	}
}
