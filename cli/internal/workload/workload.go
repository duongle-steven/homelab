// Package workload is what lab does to the readers of a secret after it changed: make their ExternalSecrets read
// again, restart their workloads and wait until they run.
package workload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

// ExternalSecrets is External Secrets Operator's resource.
var ExternalSecrets = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}

// Client acts on one cluster.
type Client struct {
	Kube kubernetes.Interface
	Dyn  dynamic.Interface
	// Poll is how often a wait looks again, Wait how long one wait may take
	Poll time.Duration
	Wait time.Duration
}

// Ref names a Deployment or a StatefulSet.
type Ref struct{ Kind, Name string }

const (
	deployment  = "Deployment"
	statefulSet = "StatefulSet"
)

// ForceSync asks ESO to read these ExternalSecrets again (the annotation force-sync) and returns without waiting.
func (c *Client) ForceSync(ctx context.Context, namespace string, names []string) error {
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]any{"force-sync": stamp}}})
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, err := c.Dyn.Resource(ExternalSecrets).Namespace(namespace).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("patch externalsecrets.external-secrets.io %s/%s: %w", namespace, name, err)
		}
	}
	return nil
}

// RefreshExternalSecrets makes these ExternalSecrets read again and waits until each one's status.refreshTime is newer
// than before and it is Ready.
func (c *Client) RefreshExternalSecrets(ctx context.Context, namespace string, names []string) error {
	before := map[string]string{}
	for _, name := range names {
		es, err := c.Dyn.Resource(ExternalSecrets).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get externalsecrets.external-secrets.io %s/%s: %w", namespace, name, err)
		}
		before[name], _, _ = unstructured.NestedString(es.Object, "status", "refreshTime")
	}
	if err := c.ForceSync(ctx, namespace, names); err != nil {
		return err
	}
	pending := map[string]bool{}
	for _, name := range names {
		pending[name] = true
	}
	// one deadline for all of them
	return c.Until(ctx, "ExternalSecrets "+namespace+"/"+strings.Join(names, ",")+" refreshed and Ready", func() (bool, error) {
		for name := range pending {
			es, err := c.Dyn.Resource(ExternalSecrets).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if refreshed(es, before[name]) {
				delete(pending, name)
			}
		}
		return len(pending) == 0, nil
	})
}

// refreshed: a refreshTime after the one before the annotation, and Ready. ESO does not copy the annotation into the
// status and writes refreshTime in whole seconds, so a refresh in the same second as the previous one is not seen.
func refreshed(es *unstructured.Unstructured, before string) bool {
	at, _, _ := unstructured.NestedString(es.Object, "status", "refreshTime")
	now, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return false
	}
	if was, err := time.Parse(time.RFC3339Nano, before); err == nil && !now.After(was) {
		return false
	}
	conditions, _, _ := unstructured.NestedSlice(es.Object, "status", "conditions")
	for _, x := range conditions {
		cond, _ := x.(map[string]any)
		if cond["type"] == "Ready" {
			return cond["status"] == "True"
		}
	}
	return false
}

// Workloads lists every Deployment and StatefulSet of the namespace.
func (c *Client) Workloads(ctx context.Context, namespace string) ([]Ref, error) {
	apps := c.Kube.AppsV1()
	deployments, err := apps.Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	statefulSets, err := apps.StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, d := range deployments.Items {
		refs = append(refs, Ref{deployment, d.Name})
	}
	for _, s := range statefulSets.Items {
		refs = append(refs, Ref{statefulSet, s.Name})
	}
	return refs, nil
}

// Restart is kubectl rollout restart for these workloads, except the StatefulSets Kanidm's operator owns (it rolls its
// own pods).
func (c *Client) Restart(ctx context.Context, namespace string, workloads []Ref) error {
	patch := []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`, time.Now().UTC().Format(time.RFC3339)))
	apps := c.Kube.AppsV1()
	for _, w := range workloads {
		switch w.Kind {
		case deployment:
			if _, err := apps.Deployments(namespace).Patch(ctx, w.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
				return err
			}
		case statefulSet:
			s, err := apps.StatefulSets(namespace).Get(ctx, w.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if OwnedBy(s.OwnerReferences, "Kanidm") {
				continue
			}
			if _, err := apps.StatefulSets(namespace).Patch(ctx, w.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("workload %s/%s: kind %q is neither a Deployment nor a StatefulSet", namespace, w.Name, w.Kind)
		}
	}
	return nil
}

// WaitReady waits for these workloads' rollouts to be complete (every replica updated, ready and available, no old
// one left), restarting crash-looping pods of the namespace once each. A workload that is missing or cannot be read is
// not ready yet.
func (c *Client) WaitReady(ctx context.Context, namespace string, workloads []Ref) error {
	for _, w := range workloads {
		if w.Kind != deployment && w.Kind != statefulSet {
			return fmt.Errorf("workload %s/%s: kind %q is neither a Deployment nor a StatefulSet", namespace, w.Name, w.Kind)
		}
	}
	restarted := map[string]bool{}
	return c.Until(ctx, namespace+"'s workloads ready", func() (bool, error) {
		c.RestartCrashLooping(ctx, namespace, restarted)
		apps := c.Kube.AppsV1()
		for _, w := range workloads {
			if w.Kind == deployment {
				d, err := apps.Deployments(namespace).Get(ctx, w.Name, metav1.GetOptions{})
				if err != nil || !deploymentRolledOut(d) {
					return false, nil
				}
				continue
			}
			s, err := apps.StatefulSets(namespace).Get(ctx, w.Name, metav1.GetOptions{})
			if err != nil || !statefulSetRolledOut(s) {
				return false, nil
			}
		}
		return true, nil
	})
}

// WaitNamespaceReady waits until every Deployment and StatefulSet of the namespace has its replicas ready, listing them
// again at every poll (one Argo CD creates meanwhile is waited for too), restarting crash-looping pods once each.
func (c *Client) WaitNamespaceReady(ctx context.Context, namespace string) error {
	restarted := map[string]bool{}
	return c.Until(ctx, namespace+"'s workloads ready", func() (bool, error) {
		c.RestartCrashLooping(ctx, namespace, restarted)
		apps := c.Kube.AppsV1()
		deployments, err := apps.Deployments(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, d := range deployments.Items {
			if d.Status.ReadyReplicas < ptr.Deref(d.Spec.Replicas, 1) {
				return false, nil
			}
		}
		statefulSets, err := apps.StatefulSets(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, st := range statefulSets.Items {
			if st.Status.ReadyReplicas < ptr.Deref(st.Spec.Replicas, 1) {
				return false, nil
			}
		}
		return true, nil
	})
}

func deploymentRolledOut(d *appsv1.Deployment) bool {
	want := ptr.Deref(d.Spec.Replicas, 1)
	s := d.Status
	return s.ObservedGeneration >= d.Generation && s.UpdatedReplicas == want && s.ReadyReplicas == want && s.AvailableReplicas == want && s.Replicas == want
}

func statefulSetRolledOut(st *appsv1.StatefulSet) bool {
	want := ptr.Deref(st.Spec.Replicas, 1)
	s := st.Status
	return s.ObservedGeneration >= st.Generation && s.UpdatedReplicas == want && s.ReadyReplicas == want && s.CurrentRevision == s.UpdateRevision
}

// RestartCrashLooping deletes the namespace's crash-looping pods that are not in restarted yet, and adds them to it.
func (c *Client) RestartCrashLooping(ctx context.Context, namespace string, restarted map[string]bool) {
	pods, err := c.Kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	for _, p := range pods.Items {
		if !restarted[p.Name] && crashLooping(p) {
			restarted[p.Name] = true
			_ = c.Kube.CoreV1().Pods(namespace).Delete(ctx, p.Name, metav1.DeleteOptions{})
		}
	}
}

func crashLooping(p corev1.Pod) bool {
	for _, s := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
		if s.State.Waiting != nil && s.State.Waiting.Reason == "CrashLoopBackOff" {
			return true
		}
	}
	return false
}

// OwnedBy is whether one of the owner references is of this kind.
func OwnedBy(refs []metav1.OwnerReference, kind string) bool {
	for _, r := range refs {
		if r.Kind == kind {
			return true
		}
	}
	return false
}

// Until polls done every c.Poll for up to c.Wait; an error from done ends it
func (c *Client) Until(ctx context.Context, what string, done func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, c.Wait)
	defer cancel()
	for {
		ok, err := done()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("waited %s for %s", c.Wait, what)
			}
			return ctx.Err()
		case <-time.After(c.Poll):
		}
	}
}
