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
	unhealthy string // healthy fails after this node's rebuild
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
	f.record("healthy")
	last := f.calls[len(f.calls)-3]
	if f.unhealthy != "" && last == "rebuild "+f.unhealthy {
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

// One node at a time, the cluster's initializer first: each is drained, rebuilt, back Ready, the cluster healthy,
// then uncordoned, before the next is touched
func TestUpgradeOneAtATime(t *testing.T) {
	f := &fakeUpgrader{current: map[string]bool{"node-b": true}}
	if err := upgrade(t.Context(), upgradeFixture(), f, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"current node-a", "drain node-a", "rebuild node-a", "ready node-a", "healthy", "uncordon node-a",
		"current node-b",
		"current node-c", "drain node-c", "rebuild node-c", "ready node-c", "healthy", "uncordon node-c",
	}
	if !slices.Equal(f.calls, want) {
		t.Fatalf("calls\n got %v\nwant %v", f.calls, want)
	}
}

// An unhealthy cluster after a node's rebuild stops the upgrade there: that node stays cordoned, the next is untouched,
// and the error names the node
func TestUpgradeStopsOnUnhealthy(t *testing.T) {
	f := &fakeUpgrader{unhealthy: "node-a"}
	err := upgrade(t.Context(), upgradeFixture(), f, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "node-a") {
		t.Fatalf("error %v, want one naming node-a", err)
	}
	for _, call := range f.calls {
		if strings.HasSuffix(call, "node-b") || call == "uncordon node-a" {
			t.Fatalf("went on after an unhealthy cluster: %v", f.calls)
		}
	}
}
