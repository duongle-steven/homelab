package rotate

import (
	"context"
	"fmt"
	"io"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Locker keeps two rotations of one entry from running at once (the CronJob and a person): both would Prepare a new
// value, and one's Revoke could end the other's.
type Locker interface {
	// Lock takes the entry's lock for holder for ttl, renewed until release is called; it fails naming the holder.
	// The returned context ends, with a LostLockError as its cause, when another run takes the lock over.
	Lock(ctx context.Context, e Entry, holder string, ttl time.Duration) (context.Context, func(), error)
}

// LostLockError is the cause of a locked context that ended because another run took the lock over.
type LostLockError struct{ Holder string }

func (e *LostLockError) Error() string { return "lost the lock to " + e.Holder }

// LeaseLocker locks with a coordination.k8s.io Lease rotate-<name> in Namespace, which a person's lab and the
// CronJob both reach through the API server; a lock whose holder stopped renewing it expires.
type LeaseLocker struct {
	Kube      kubernetes.Interface
	Namespace string
	// Renew is how often the Lease is renewed (default a third of its ttl), Out where a failed renewal is logged
	Renew time.Duration
	Out   io.Writer
	// ReleaseFor is how long the release keeps trying while the API server does not answer (default two minutes, the
	// API server's restart after a power cut); past it the Lease is left to expire
	ReleaseFor time.Duration
}

// Lease is who holds an entry's lock: since when, its last renewal and when it expires without one
type Lease struct {
	Holder                   string
	Acquired, Renewed, Until time.Time
}

// apiTimeout bounds each renewal and each try of the release, so neither stalls on a hung API call
const apiTimeout = 30 * time.Second

// Lock takes the Lease, or takes over one that expired.
func (l *LeaseLocker) Lock(ctx context.Context, e Entry, holder string, ttl time.Duration) (context.Context, func(), error) {
	name := "rotate-" + e.Name
	leases := l.Kube.CoordinationV1().Leases(l.Namespace)
	now := metav1.NewMicroTime(time.Now())
	seconds := max(int32(ttl.Seconds()), 1)
	spec := coordinationv1.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &seconds, AcquireTime: &now, RenewTime: &now}
	lease, err := leases.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		lease, err = leases.Create(ctx, &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: l.Namespace}, Spec: spec}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return nil, nil, fmt.Errorf("another rotation of %s took its lock (Lease %s/%s) just now", e.Name, l.Namespace, name)
		}
	case err != nil:
	default:
		if by, until, ok := held(lease, now.Time); ok {
			return nil, nil, fmt.Errorf("%s is being rotated by %s (Lease %s/%s): wait for it, or, if that run died, for "+
				"its lock to expire at %s, or end it now with lab secrets force-unlock %s %s", e.Name, by, l.Namespace, name,
				until.Local().Format(time.DateTime), e.Name, by)
		}
		lease.Spec = spec
		// The resourceVersion of the expired Lease we read: a run taking it at the same time gets a conflict
		lease, err = leases.Update(ctx, lease, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return nil, nil, fmt.Errorf("another rotation of %s took its expired lock (Lease %s/%s) just now", e.Name, l.Namespace, name)
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lock %s with Lease %s/%s: %w", e.Name, l.Namespace, name, err)
	}
	out := l.Out
	if out == nil {
		out = io.Discard
	}
	// Renewing every third of the ttl tolerates a missed renewal and clocks a little apart: another run judges the
	// Lease expired by its own clock against our RenewTime
	every := l.Renew
	if every <= 0 {
		every = ttl / 3
	}
	locked, cancel := context.WithCancelCause(ctx)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			renewed := lease.DeepCopy()
			renewed.Spec.RenewTime = &metav1.MicroTime{Time: time.Now()}
			callCtx, callCancel := context.WithTimeout(context.Background(), apiTimeout)
			updated, err := leases.Update(callCtx, renewed, metav1.UpdateOptions{})
			switch {
			case err == nil:
				lease = updated
			case apierrors.IsConflict(err):
				current, getErr := leases.Get(callCtx, name, metav1.GetOptions{})
				if getErr == nil && current.Spec.HolderIdentity != nil && *current.Spec.HolderIdentity == holder {
					// Still ours: an earlier renewal reached the API server but its answer did not reach us
					lease = current
					break
				}
				other := "another run"
				if getErr == nil && current.Spec.HolderIdentity != nil {
					other = *current.Spec.HolderIdentity
				}
				callCancel()
				cancel(&LostLockError{Holder: other})
				return
			default:
				// Retried at the next tick; the lock lasts ttl from the last renewal
				fmt.Fprintf(out, "%s: renew the lock (Lease %s/%s) failed: %v\n", e.Name, l.Namespace, name, err)
			}
			callCancel()
		}
	}()
	release := func() {
		close(stop)
		<-stopped
		defer cancel(nil)
		l.release(context.WithoutCancel(ctx), name, holder)
	}
	return locked, release, nil
}

// release deletes the Lease while holder still holds it, trying again while the API server does not answer, for
// ReleaseFor; errors are dropped, the Lease then expires after its ttl
func (l *LeaseLocker) release(ctx context.Context, name, holder string) {
	leases := l.Kube.CoordinationV1().Leases(l.Namespace)
	period := l.ReleaseFor
	if period <= 0 {
		period = 2 * time.Minute
	}
	deadline := time.Now().Add(period)
	wait := period / 60
	for {
		callDeadline := time.Now().Add(apiTimeout)
		if deadline.Before(callDeadline) {
			callDeadline = deadline
		}
		callCtx, callCancel := context.WithDeadline(ctx, callDeadline)
		current, err := leases.Get(callCtx, name, metav1.GetOptions{})
		if err == nil {
			if current.Spec.HolderIdentity == nil || *current.Spec.HolderIdentity != holder {
				callCancel()
				return
			}
			// Only the version we read: a takeover is an Update, so the UID stays and protects nothing
			rv := current.ResourceVersion
			err = leases.Delete(callCtx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}})
		}
		callCancel()
		switch {
		case err == nil, apierrors.IsNotFound(err):
			return
		case apierrors.IsConflict(err):
			// Renewed or taken over since we read it: read it again
			continue
		}
		if time.Now().Add(wait).After(deadline) {
			return
		}
		time.Sleep(wait)
		wait = min(2*wait, 15*time.Second)
	}
}

// Holder reads who holds the lock of the entry name.
func (l *LeaseLocker) Holder(ctx context.Context, name string) (Lease, error) {
	_, held, err := l.get(ctx, name)
	return held, err
}

// ForceUnlock deletes the lock of the entry name that holder holds, like tofu force-unlock: for a run that died
// without releasing it. It refuses when another run holds the lock, so a live run's lock stays.
func (l *LeaseLocker) ForceUnlock(ctx context.Context, name, holder string) (Lease, error) {
	current, held, err := l.get(ctx, name)
	if err != nil {
		return Lease{}, err
	}
	if held.Holder != holder {
		return Lease{}, fmt.Errorf("%s is locked by %q, not %q: nothing unlocked", name, held.Holder, holder)
	}
	rv := current.ResourceVersion
	err = l.Kube.CoordinationV1().Leases(l.Namespace).Delete(ctx, current.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}})
	if apierrors.IsConflict(err) {
		return Lease{}, fmt.Errorf("the lock of %s changed since it was read (renewed or taken over): nothing unlocked", name)
	}
	if err != nil {
		return Lease{}, fmt.Errorf("delete the lock of %s (Lease %s/%s): %w", name, l.Namespace, current.Name, err)
	}
	return held, nil
}

// get reads the Lease of the entry name and who it names
func (l *LeaseLocker) get(ctx context.Context, name string) (*coordinationv1.Lease, Lease, error) {
	lease, err := l.Kube.CoordinationV1().Leases(l.Namespace).Get(ctx, "rotate-"+name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, Lease{}, fmt.Errorf("%s is not locked (no Lease %s/rotate-%s)", name, l.Namespace, name)
	}
	if err != nil {
		return nil, Lease{}, fmt.Errorf("read the lock of %s (Lease %s/rotate-%s): %w", name, l.Namespace, name, err)
	}
	s := lease.Spec
	var held Lease
	if s.HolderIdentity != nil {
		held.Holder = *s.HolderIdentity
	}
	if s.AcquireTime != nil {
		held.Acquired = s.AcquireTime.Time
	}
	if s.RenewTime != nil {
		held.Renewed = s.RenewTime.Time
		if s.LeaseDurationSeconds != nil {
			held.Until = held.Renewed.Add(time.Duration(*s.LeaseDurationSeconds) * time.Second)
		}
	}
	return lease, held, nil
}

// held reports the holder of an unexpired Lease and when it expires
func held(lease *coordinationv1.Lease, now time.Time) (string, time.Time, bool) {
	s := lease.Spec
	if s.HolderIdentity == nil || *s.HolderIdentity == "" || s.RenewTime == nil || s.LeaseDurationSeconds == nil {
		return "", time.Time{}, false
	}
	until := s.RenewTime.Add(time.Duration(*s.LeaseDurationSeconds) * time.Second)
	return *s.HolderIdentity, until, until.After(now)
}
