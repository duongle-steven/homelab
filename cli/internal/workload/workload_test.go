package workload

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

func externalSecret(ns, name, refreshTime string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetAPIVersion("external-secrets.io/v1")
	u.SetKind("ExternalSecret")
	u.SetNamespace(ns)
	u.SetName(name)
	if refreshTime != "" {
		_ = unstructured.SetNestedField(u.Object, refreshTime, "status", "refreshTime")
		_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
	}
	return u
}

func dynFake(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{ExternalSecrets: "ExternalSecretList"}, objects...)
}

// ESO answers a force-sync by refreshing: the fake does it after a delay
func TestRefreshExternalSecretsWaitsForRefresh(t *testing.T) {
	dyn := dynFake(externalSecret("ntfy", "ntfy-users", "2026-10-23T10:00:00Z"))
	c := &Client{Dyn: dyn, Kube: fake.NewClientset(), Poll: time.Millisecond, Wait: 2 * time.Second}
	var mu sync.Mutex
	var annotated time.Time
	dyn.PrependReactor("patch", "externalsecrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if !strings.Contains(string(a.(k8stesting.PatchAction).GetPatch()), "force-sync") {
			return false, nil, nil
		}
		mu.Lock()
		annotated = time.Now()
		mu.Unlock()
		go func() {
			time.Sleep(50 * time.Millisecond)
			es := externalSecret("ntfy", "ntfy-users", "2026-10-23T10:05:00Z")
			_, _ = dyn.Resource(ExternalSecrets).Namespace("ntfy").Update(context.Background(), es, metav1.UpdateOptions{})
		}()
		return false, nil, nil
	})
	if err := c.RefreshExternalSecrets(context.Background(), "ntfy", []string{"ntfy-users"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if annotated.IsZero() || time.Since(annotated) < 50*time.Millisecond {
		t.Fatalf("returned %v after the annotation, before the refresh", time.Since(annotated))
	}
}

func TestRefreshExternalSecretsTimesOutWithoutRefresh(t *testing.T) {
	c := &Client{Dyn: dynFake(externalSecret("ntfy", "ntfy-users", "2026-10-23T10:00:00Z")), Kube: fake.NewClientset(), Poll: time.Millisecond, Wait: 50 * time.Millisecond}
	err := c.RefreshExternalSecrets(context.Background(), "ntfy", []string{"ntfy-users"})
	if err == nil || !strings.Contains(err.Error(), "ntfy/ntfy-users") {
		t.Fatalf("err = %v, want a timeout naming the ExternalSecret", err)
	}
}

func TestRestartSkipsKanidmStatefulSets(t *testing.T) {
	kube := fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "app"}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "db"}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "kanidm", OwnerReferences: []metav1.OwnerReference{{Kind: "Kanidm", Name: "kanidm"}}}},
	)
	var patched []string
	kube.PrependReactor("patch", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		patched = append(patched, a.GetResource().Resource+"/"+a.(k8stesting.PatchAction).GetName()+" "+string(a.(k8stesting.PatchAction).GetPatch()))
		return false, nil, nil
	})
	c := &Client{Kube: kube, Poll: time.Millisecond, Wait: time.Second}
	refs, err := c.Workloads(context.Background(), "ns")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Restart(context.Background(), "ns", refs); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(patched, "\n")
	if len(patched) != 2 || !strings.Contains(got, "deployments/app") || !strings.Contains(got, "statefulsets/db") || !strings.Contains(got, "restartedAt") || strings.Contains(got, "kanidm") {
		t.Fatalf("patched:\n%s", got)
	}
}

func TestWaitReadyRestartsCrashLoopingPodsOnce(t *testing.T) {
	deployments := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "app"}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1)}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "app-0"}, Status: corev1.PodStatus{
		ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}},
	}}
	kube := fake.NewClientset(deployments, pod)
	deletes := 0
	kube.PrependReactor("delete", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		deletes++
		// the fake holds its lock inside a reaction: the replacement pod becomes ready from another goroutine
		go func() {
			d := deployments.DeepCopy()
			d.Status.ReadyReplicas = 1
			_, _ = kube.AppsV1().Deployments("ns").UpdateStatus(context.Background(), d, metav1.UpdateOptions{})
		}()
		return false, nil, nil
	})
	c := &Client{Kube: kube, Poll: time.Millisecond, Wait: 2 * time.Second}
	if err := c.WaitReady(context.Background(), "ns", []Ref{{"Deployment", "app"}}); err != nil {
		t.Fatal(err)
	}
	if deletes != 1 {
		t.Fatalf("pod deleted %d times, want once", deletes)
	}
}
