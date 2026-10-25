package rotate

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// CNPGPassword replaces a CloudNativePG database owner's password: the cluster's instance manager applies the app
// Secret's password to the role when the Secret changes, but keeps the keys derived from it (Settings derived: uri,
// pgpass...), so the rotation rewrites them too. PostgreSQL keeps open sessions; the readers restart on the new value.
type CNPGPassword struct {
	Kube      kubernetes.Interface
	Store     KV
	Workloads Readers
	Random    func(Entry) (string, error) // Password when nil
}

func (m CNPGPassword) Prepare(ctx context.Context, e Entry) error {
	value, err := pending(ctx, m.Store, m.Random, e)
	if err != nil {
		return hide(err, value)
	}
	ns, name := e.Settings["namespace"], e.Settings["secret"]
	secrets := m.Kube.CoreV1().Secrets(ns)
	s, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Secret %s/%s: %w", ns, name, err)
	}
	old := string(s.Data["password"])
	if old == value {
		return nil
	}
	if old == "" {
		return fmt.Errorf("Secret %s/%s has no password", ns, name)
	}
	next := s.DeepCopy()
	next.Data["password"] = []byte(value)
	for _, key := range strings.Split(e.Settings["derived"], ",") {
		if v, ok := s.Data[key]; ok {
			next.Data[key] = []byte(strings.ReplaceAll(string(v), old, value))
		}
	}
	if _, err := secrets.Update(ctx, next, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update Secret %s/%s: %w", ns, name, hide(err, old, value))
	}
	return nil
}

func (m CNPGPassword) Switch(ctx context.Context, e Entry) error {
	for _, r := range namespaced(e) {
		if len(r.Workloads) > 0 {
			if err := m.Workloads.Restart(ctx, r.Namespace, r.Workloads); err != nil {
				return fmt.Errorf("restart %s's workloads: %w", r.Namespace, err)
			}
		}
	}
	return nil
}

func (m CNPGPassword) Verify(ctx context.Context, e Entry) error {
	for _, r := range namespaced(e) {
		if len(r.Workloads) > 0 {
			if err := m.Workloads.WaitReady(ctx, r.Namespace, r.Workloads); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m CNPGPassword) Revoke(ctx context.Context, e Entry) error { return dropPending(ctx, m.Store, e) }
