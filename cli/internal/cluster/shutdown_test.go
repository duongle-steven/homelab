package cluster

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeStopper struct {
	saved    *marker
	replicas int
	crons    []cronJob
	jobs     []string
	noCeph   bool
	failStop bool
	calls    []string
}

func (f *fakeStopper) readMarker(context.Context) (marker, bool, error) {
	if f.saved == nil {
		return marker{}, false, nil
	}
	return *f.saved, true, nil
}
func (f *fakeStopper) writeMarker(_ context.Context, m marker) error {
	f.saved = &m
	f.calls = append(f.calls, "marker "+strconv.Itoa(m.Replicas)+" "+strings.Join(m.CronJobs, ","))
	return nil
}
func (f *fakeStopper) controllerReplicas(context.Context) (int, error) { return f.replicas, nil }
func (f *fakeStopper) boots(context.Context) (map[string]string, error) {
	return map[string]string{"homelab-1": "boot-1"}, nil
}
func (f *fakeStopper) stopController(context.Context) error {
	f.calls = append(f.calls, "stop controller")
	if f.failStop {
		return errors.New("forbidden")
	}
	return nil
}
func (f *fakeStopper) cronJobs(context.Context) ([]cronJob, error) { return f.crons, nil }
func (f *fakeStopper) suspend(_ context.Context, name string) error {
	f.calls = append(f.calls, "suspend "+name)
	return nil
}
func (f *fakeStopper) runningJobs(context.Context) ([]string, error) { return f.jobs, nil }
func (f *fakeStopper) noout(context.Context) error {
	f.calls = append(f.calls, "noout")
	if f.noCeph {
		return errNoCeph
	}
	return nil
}
func (f *fakeStopper) powerOff(context.Context) error {
	f.calls = append(f.calls, "power off")
	return nil
}

func TestShutdownOrder(t *testing.T) {
	f := &fakeStopper{replicas: 1, crons: []cronJob{{name: "a/one"}, {name: "b/two"}}}
	if err := shutdown(t.Context(), f, time.Minute, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"marker 1 ",
		"stop controller",
		"marker 1 a/one",
		"suspend a/one",
		"marker 1 a/one,b/two",
		"suspend b/two",
		"noout",
		"power off",
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls\n%q\nwant\n%q", f.calls, want)
	}
	if f.saved.Boots["homelab-1"] != "boot-1" {
		t.Fatalf("boots %v", f.saved.Boots)
	}
}

func TestShutdownSkipsSuspendedAndKept(t *testing.T) {
	f := &fakeStopper{replicas: 1, crons: []cronJob{{name: "a/held", suspended: true}, {name: "istio-system/watchdog", kept: true}}}
	if err := shutdown(t.Context(), f, time.Minute, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "suspend") {
			t.Errorf("unexpected %s", call)
		}
	}
	if len(f.saved.CronJobs) != 0 {
		t.Fatalf("marker names %v", f.saved.CronJobs)
	}
}

// A run after a partial one sees the controller at zero and the CronJob suspended: what it found then is what startup restores
func TestShutdownAgainKeepsTheMarker(t *testing.T) {
	f := &fakeStopper{
		replicas: 0,
		saved:    &marker{Replicas: 2, CronJobs: []string{"a/b"}, Boots: map[string]string{"homelab-1": "first"}},
		crons:    []cronJob{{name: "a/b", suspended: true}, {name: "c/d"}},
	}
	if err := shutdown(t.Context(), f, time.Minute, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if f.saved.Replicas != 2 || !reflect.DeepEqual(f.saved.CronJobs, []string{"a/b", "c/d"}) || f.saved.Boots["homelab-1"] != "first" {
		t.Fatalf("marker %+v", *f.saved)
	}
}

func TestShutdownJobWaitIsBounded(t *testing.T) {
	f := &fakeStopper{replicas: 1, jobs: []string{"backup/long"}}
	var progress bytes.Buffer
	if err := shutdown(t.Context(), f, 10*time.Millisecond, &progress); err != nil {
		t.Fatal(err)
	}
	if f.calls[len(f.calls)-1] != "power off" {
		t.Fatalf("calls %q", f.calls)
	}
	if !strings.Contains(progress.String(), "backup/long") {
		t.Fatalf("progress %q", progress.String())
	}
}

func TestShutdownNoCeph(t *testing.T) {
	f := &fakeStopper{replicas: 1, noCeph: true}
	if err := shutdown(t.Context(), f, time.Minute, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if f.calls[len(f.calls)-1] != "power off" {
		t.Fatalf("calls %q", f.calls)
	}
}

func TestShutdownFailureSaysStartup(t *testing.T) {
	f := &fakeStopper{replicas: 1, failStop: true}
	err := shutdown(t.Context(), f, time.Minute, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "lab cluster startup") {
		t.Fatalf("got %v", err)
	}
	for _, call := range f.calls {
		if call == "power off" {
			t.Fatal("powered off after a failure")
		}
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	m := marker{Replicas: 2, CronJobs: []string{"a/b", "c/d"}, Boots: map[string]string{"homelab-1": "x", "homelab-2": "y"}}
	got, err := parseMarker(m.data())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Fatalf("got %+v", got)
	}
	empty, err := parseMarker(marker{Replicas: 1}.data())
	if err != nil || len(empty.CronJobs) != 0 {
		t.Fatalf("empty: %+v, %v", empty, err)
	}
}

func TestStartupTriesTheNextNode(t *testing.T) {
	nodes := []host{{name: "a"}, {name: "b"}, {name: "c"}}
	var tried []string
	err := startup(nodes, func(node host) error {
		tried = append(tried, node.name)
		if node.name == "a" {
			return errUnreachable
		}
		return nil
	})
	if err != nil || strings.Join(tried, ",") != "a,b" {
		t.Fatalf("tried %v, err %v", tried, err)
	}
}

// The script failing is its answer: another node would say the same
func TestStartupStopsAtAFailure(t *testing.T) {
	nodes := []host{{name: "a"}, {name: "b"}}
	var tried []string
	want := errors.New("exit status 1")
	err := startup(nodes, func(node host) error {
		tried = append(tried, node.name)
		return want
	})
	if !errors.Is(err, want) || len(tried) != 1 {
		t.Fatalf("tried %v, err %v", tried, err)
	}
}

func TestStartupNoNodeAnswers(t *testing.T) {
	err := startup([]host{{name: "a"}}, func(host) error { return errUnreachable })
	if err == nil || !strings.Contains(err.Error(), "no node answered") {
		t.Fatalf("got %v", err)
	}
}
