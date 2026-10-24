package rotate

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// KanidmOAuth2Clients are kaniop's OAuth2 clients.
var KanidmOAuth2Clients = schema.GroupVersionResource{Group: "kaniop.rs", Version: "v1beta1", Resource: "kanidmoauth2clients"}

// forceSecretRotation makes kaniop reset a confidential client's secret once; kaniop removes it afterwards
const forceSecretRotation = "kaniop.rs/force-secret-rotation"

// Waiter polls until done (workload.Client.Until).
type Waiter interface {
	Until(ctx context.Context, what string, done func() (bool, error)) error
}

// KaniopClient gives a Kanidm OAuth2 client (Settings client, in Settings namespace) a new secret through kaniop's
// annotation: Kanidm keeps one secret per client, so the old one ends at once and logins fail until the readers
// restart on the new one (Argo CD reads its Secret at each login and has no workload to restart).
type KaniopClient struct {
	Kube      kubernetes.Interface
	Dyn       dynamic.Interface
	Store     KV
	Workloads Readers
	Waits     Waiter
}

func credentials(e Entry) string { return e.Settings["client"] + "-kanidm-oauth2-credentials" }

func (m KaniopClient) Prepare(ctx context.Context, e Entry) error {
	ns, secret := e.Settings["namespace"], credentials(e)
	state, err := readState(ctx, m.Store, e)
	if err != nil {
		return err
	}
	current, err := m.Kube.CoreV1().Secrets(ns).Get(ctx, secret, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Secret %s/%s: %w", ns, secret, err)
	}
	before := state["secret-version"]
	if before == "" {
		before = current.ResourceVersion
		if err := writeState(ctx, m.Store, e, map[string]string{"secret-version": before}); err != nil {
			return err
		}
	}
	if current.ResourceVersion == before {
		patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, forceSecretRotation, time.Now().UTC().Format(time.RFC3339))
		if _, err := m.Dyn.Resource(KanidmOAuth2Clients).Namespace(ns).Patch(ctx, e.Settings["client"], types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("ask kaniop for a new secret of %s/%s: %w", ns, e.Settings["client"], err)
		}
	}
	return m.Waits.Until(ctx, "kaniop's new Secret "+ns+"/"+secret, func() (bool, error) {
		s, err := m.Kube.CoreV1().Secrets(ns).Get(ctx, secret, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return s.ResourceVersion != before, nil
	})
}

func (m KaniopClient) Switch(ctx context.Context, e Entry) error {
	for _, r := range namespaced(e) {
		if len(r.Workloads) > 0 {
			if err := m.Workloads.Restart(ctx, r.Namespace, r.Workloads); err != nil {
				return fmt.Errorf("restart %s's workloads: %w", r.Namespace, err)
			}
		}
	}
	return nil
}

func (m KaniopClient) Verify(ctx context.Context, e Entry) error {
	for _, r := range namespaced(e) {
		if len(r.Workloads) > 0 {
			if err := m.Workloads.WaitReady(ctx, r.Namespace, r.Workloads); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m KaniopClient) Revoke(ctx context.Context, e Entry) error {
	return writeState(ctx, m.Store, e, nil)
}

// KanidmAdmin replaces Kanidm's admin and idm_admin passwords: kaniop writes new ones when their Secret is deleted.
// Only lab, the tests and kaniop use them (class 1).
type KanidmAdmin struct {
	Kube  kubernetes.Interface
	Store KV
	Waits Waiter
}

const kanidmAdminSecret = "kanidm-admin-passwords"

func (m KanidmAdmin) Prepare(ctx context.Context, e Entry) error {
	ns := e.Settings["namespace"]
	secrets := m.Kube.CoreV1().Secrets(ns)
	state, err := readState(ctx, m.Store, e)
	if err != nil {
		return err
	}
	before := state["secret-uid"]
	if before == "" {
		current, err := secrets.Get(ctx, kanidmAdminSecret, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("Secret %s/%s: %w", ns, kanidmAdminSecret, err)
		}
		before = string(current.UID)
		if err := writeState(ctx, m.Store, e, map[string]string{"secret-uid": before}); err != nil {
			return err
		}
	}
	current, err := secrets.Get(ctx, kanidmAdminSecret, metav1.GetOptions{})
	switch {
	case err == nil && string(current.UID) == before:
		if err := secrets.Delete(ctx, kanidmAdminSecret, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &current.UID}}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete Secret %s/%s: %w", ns, kanidmAdminSecret, err)
		}
	case err != nil && !apierrors.IsNotFound(err):
		return fmt.Errorf("Secret %s/%s: %w", ns, kanidmAdminSecret, err)
	}
	return m.Waits.Until(ctx, "kaniop's new Secret "+ns+"/"+kanidmAdminSecret, func() (bool, error) {
		s, err := secrets.Get(ctx, kanidmAdminSecret, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return string(s.UID) != before, nil
	})
}

func (m KanidmAdmin) Switch(context.Context, Entry) error { return nil }

func (m KanidmAdmin) Verify(ctx context.Context, e Entry) error {
	if _, err := m.Kube.CoreV1().Secrets(e.Settings["namespace"]).Get(ctx, kanidmAdminSecret, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("Secret %s/%s: %w", e.Settings["namespace"], kanidmAdminSecret, err)
	}
	return nil
}

func (m KanidmAdmin) Revoke(ctx context.Context, e Entry) error {
	return writeState(ctx, m.Store, e, nil)
}
