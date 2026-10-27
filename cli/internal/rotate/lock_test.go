package rotate

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// lockFor takes grafana.admin's lock for holder and returns its release
func lockFor(t *testing.T, locker *LeaseLocker, holder string) func() {
	t.Helper()
	_, release, err := locker.Lock(context.Background(), generated("grafana.admin"), holder, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

// A run killed with the node leaves its Lease: force-unlock removes it, naming what it removed
func TestForceUnlock(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientset()
	locker := &LeaseLocker{Kube: kube, Namespace: "secrets-rotation"}
	lockFor(t, locker, "steven@workstation/1")
	held, err := locker.Holder(ctx, "grafana.admin")
	if err != nil || held.Holder != "steven@workstation/1" || held.Renewed.IsZero() || !held.Until.After(held.Renewed) {
		t.Fatalf("Holder = %+v, %v", held, err)
	}
	removed, err := locker.ForceUnlock(ctx, "grafana.admin", "steven@workstation/1")
	if err != nil || removed.Holder != "steven@workstation/1" {
		t.Fatalf("ForceUnlock = %+v, %v", removed, err)
	}
	if _, err := kube.Tracker().Get(leaseGVR, "secrets-rotation", "rotate-grafana.admin"); !apierrors.IsNotFound(err) {
		t.Fatalf("the Lease is still there: %v", err)
	}
	if _, err := locker.ForceUnlock(ctx, "grafana.admin", "steven@workstation/1"); err == nil || !strings.Contains(err.Error(), "not locked") {
		t.Fatalf("a second force-unlock: %v", err)
	}
}

// The run that died was followed by a live one: force-unlock with the dead run's holder must not end the live run's lock
func TestForceUnlockRefusesAnotherHolder(t *testing.T) {
	kube := fake.NewClientset()
	locker := &LeaseLocker{Kube: kube, Namespace: "secrets-rotation"}
	lockFor(t, locker, "rotation-job/2")
	_, err := locker.ForceUnlock(context.Background(), "grafana.admin", "steven@workstation/1")
	if err == nil || !strings.Contains(err.Error(), "rotation-job/2") {
		t.Fatalf("error = %v", err)
	}
	if got := *leaseObject(t, kube).Spec.HolderIdentity; got != "rotation-job/2" {
		t.Fatalf("holder = %q", got)
	}
}

// The API server restarting (a power cut) fails the first deletes: the release tries again until one succeeds
func TestLockReleaseRetries(t *testing.T) {
	kube := fake.NewClientset()
	locker := &LeaseLocker{Kube: kube, Namespace: "secrets-rotation", ReleaseFor: 5 * time.Second}
	release := lockFor(t, locker, "steven@workstation/1")
	var deletes atomic.Int32
	kube.PrependReactor("delete", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		if deletes.Add(1) <= 2 {
			return true, nil, apierrors.NewServiceUnavailable("apiserver not ready")
		}
		return false, nil, nil
	})
	release()
	if _, err := kube.Tracker().Get(leaseGVR, "secrets-rotation", "rotate-grafana.admin"); !apierrors.IsNotFound(err) || deletes.Load() != 3 {
		t.Fatalf("after %d deletes the Lease is %v", deletes.Load(), err)
	}
}

// An API server down for longer: the release gives up and the Lease expires as before
func TestLockReleaseRetriesThenGivesUp(t *testing.T) {
	kube := fake.NewClientset()
	locker := &LeaseLocker{Kube: kube, Namespace: "secrets-rotation", ReleaseFor: 50 * time.Millisecond}
	release := lockFor(t, locker, "steven@workstation/1")
	kube.PrependReactor("get", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("apiserver not ready")
	})
	start := time.Now()
	release()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("release took %s", elapsed)
	}
}
