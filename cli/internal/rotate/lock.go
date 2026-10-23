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
}

// apiTimeout bounds each renewal and the release, so neither stalls on a hung API call
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
				"its lock to expire at %s", e.Name, by, l.Namespace, name, until.Local().Format(time.DateTime))
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
				other := "another run"
				if current, err := leases.Get(callCtx, name, metav1.GetOptions{}); err == nil && current.Spec.HolderIdentity != nil {
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
		ctx, timeout := context.WithTimeout(context.WithoutCancel(ctx), apiTimeout)
		defer timeout()
		current, err := leases.Get(ctx, name, metav1.GetOptions{})
		if err == nil && current.Spec.HolderIdentity != nil && *current.Spec.HolderIdentity == holder {
			// Only the version we read: a takeover is an Update, so the UID stays and protects nothing. Left
			// behind on an error, the Lease expires after ttl
			rv := current.ResourceVersion
			_ = leases.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}})
		}
	}
	return locked, release, nil
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
