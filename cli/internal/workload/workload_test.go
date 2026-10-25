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
	"k8s.io/apimachinery/pkg/types"
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
	deployments := rolledOut("app", 0, 0)
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
			d.Status.ReadyReplicas, d.Status.AvailableReplicas, d.Status.UpdatedReplicas = 1, 1, 1
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

func rolledOut(name string, ready, updated int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name, Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr.To[int32](1)},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, ReadyReplicas: ready, AvailableReplicas: ready, UpdatedReplicas: updated},
	}
}

// Right after Restart the old pods are still ready: only a finished rollout counts
func TestWaitReadyRequiresCompleteRollout(t *testing.T) {
	for name, tc := range map[string]struct {
		stale, done runtime.Object
	}{
		"deployment": {
			stale: rolledOut("app", 1, 0),
			done:  rolledOut("app", 1, 1),
		},
		"statefulset": {
			stale: &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "app", Generation: 2}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1)},
				Status: appsv1.StatefulSetStatus{ObservedGeneration: 2, ReadyReplicas: 1, UpdatedReplicas: 0, CurrentRevision: "a", UpdateRevision: "b"}},
			done: &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "app", Generation: 2}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1)},
				Status: appsv1.StatefulSetStatus{ObservedGeneration: 2, ReadyReplicas: 1, UpdatedReplicas: 1, CurrentRevision: "b", UpdateRevision: "b"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			kind := map[string]string{"deployment": "Deployment", "statefulset": "StatefulSet"}[name]
			kube := fake.NewClientset(tc.stale)
			c := &Client{Kube: kube, Poll: time.Millisecond, Wait: 50 * time.Millisecond}
			if err := c.WaitReady(context.Background(), "ns", []Ref{{kind, "app"}}); err == nil {
				t.Fatal("old pods still ready, rollout not done: WaitReady returned")
			}
			c.Wait = 2 * time.Second
			go func() {
				time.Sleep(30 * time.Millisecond)
				if d, ok := tc.done.(*appsv1.Deployment); ok {
					_, _ = kube.AppsV1().Deployments("ns").Update(context.Background(), d, metav1.UpdateOptions{})
				} else {
					_, _ = kube.AppsV1().StatefulSets("ns").Update(context.Background(), tc.done.(*appsv1.StatefulSet), metav1.UpdateOptions{})
				}
			}()
			if err := c.WaitReady(context.Background(), "ns", []Ref{{kind, "app"}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A NotFound (recreated by Argo CD) is not ready yet, not an error
func TestWaitReadyToleratesARecreatedWorkload(t *testing.T) {
	kube := fake.NewClientset()
	c := &Client{Kube: kube, Poll: time.Millisecond, Wait: 2 * time.Second}
	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = kube.AppsV1().Deployments("ns").Create(context.Background(), rolledOut("app", 1, 1), metav1.CreateOptions{})
	}()
	if err := c.WaitReady(context.Background(), "ns", []Ref{{"Deployment", "app"}}); err != nil {
		t.Fatal(err)
	}
}

// backup's wait: the namespace's workloads are listed at every poll, so one created later is waited for
func TestWaitNamespaceReadyRelists(t *testing.T) {
	kube := fake.NewClientset(rolledOut("first", 0, 0))
	c := &Client{Kube: kube, Poll: time.Millisecond, Wait: 2 * time.Second}
	go func() {
		time.Sleep(30 * time.Millisecond)
		// "late" appears in the poll that sees "first" ready
		_, _ = kube.AppsV1().Deployments("ns").Create(context.Background(), rolledOut("late", 0, 0), metav1.CreateOptions{})
		_, _ = kube.AppsV1().Deployments("ns").Update(context.Background(), rolledOut("first", 1, 1), metav1.UpdateOptions{})
		time.Sleep(60 * time.Millisecond)
		_, _ = kube.AppsV1().Deployments("ns").Update(context.Background(), rolledOut("late", 1, 1), metav1.UpdateOptions{})
	}()
	start := time.Now()
	if err := c.WaitNamespaceReady(context.Background(), "ns"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 85*time.Millisecond {
		t.Fatalf("returned after %v, before the late workload was ready", time.Since(start))
	}
}

func TestUnknownKindNamesIt(t *testing.T) {
	c := &Client{Kube: fake.NewClientset(), Poll: time.Millisecond, Wait: 50 * time.Millisecond}
	refs := []Ref{{"DaemonSet", "x"}}
	if err := c.Restart(context.Background(), "ns", refs); err == nil || !strings.Contains(err.Error(), "DaemonSet") {
		t.Fatalf("Restart err = %v", err)
	}
	if err := c.WaitReady(context.Background(), "ns", refs); err == nil || !strings.Contains(err.Error(), "DaemonSet") {
		t.Fatalf("WaitReady err = %v", err)
	}
}

func TestRefreshedComparesTimes(t *testing.T) {
	// as text "…:00.5Z" sorts before "…:00Z"
	if !refreshed(externalSecret("ns", "x", "2026-10-23T10:00:00.5Z"), "2026-10-23T10:00:00Z") {
		t.Fatal("a refresh half a second later was not seen")
	}
	if refreshed(externalSecret("ns", "x", "2026-10-23T10:00:00Z"), "2026-10-23T10:00:00Z") {
		t.Fatal("the same refresh counted as new")
	}
}

func TestRefreshExternalSecretsOneDeadline(t *testing.T) {
	dyn := dynFake(externalSecret("ns", "a", "2026-10-23T10:00:00Z"), externalSecret("ns", "b", "2026-10-23T10:00:00Z"))
	c := &Client{Dyn: dyn, Kube: fake.NewClientset(), Poll: time.Millisecond, Wait: 100 * time.Millisecond}
	go func() {
		time.Sleep(70 * time.Millisecond)
		_, _ = dyn.Resource(ExternalSecrets).Namespace("ns").Update(context.Background(), externalSecret("ns", "a", "2026-10-23T10:05:00Z"), metav1.UpdateOptions{})
	}()
	start := time.Now()
	if err := c.RefreshExternalSecrets(context.Background(), "ns", []string{"a", "b"}); err == nil {
		t.Fatal("b never refreshed, no error")
	}
	if time.Since(start) > 140*time.Millisecond {
		t.Fatalf("waited %v for two ExternalSecrets, want one Wait of 100ms", time.Since(start))
	}
}

func TestRunningPod(t *testing.T) {
	ctx := context.Background()
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "paperless"}}
	pod := func(name string, phase corev1.PodPhase, ready corev1.ConditionStatus, labels map[string]string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "paperless", Labels: labels},
			Status:     corev1.PodStatus{Phase: phase, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}}},
		}
	}
	app := map[string]string{"app": "paperless"}
	kube := fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "paperless", Namespace: "paperless"}, Spec: appsv1.DeploymentSpec{Selector: selector}},
		pod("starting", corev1.PodRunning, corev1.ConditionFalse, app),
		pod("other", corev1.PodRunning, corev1.ConditionTrue, map[string]string{"app": "redis"}),
		pod("done", corev1.PodSucceeded, corev1.ConditionFalse, app),
	)
	if name, err := RunningPod(ctx, kube, "paperless", Ref{"Deployment", "paperless"}); err == nil {
		t.Fatalf("no pod is ready, yet RunningPod chose %s", name)
	}
	_, _ = kube.CoreV1().Pods("paperless").Create(ctx, pod("ready", corev1.PodRunning, corev1.ConditionTrue, app), metav1.CreateOptions{})
	if name, err := RunningPod(ctx, kube, "paperless", Ref{"Deployment", "paperless"}); err != nil || name != "ready" {
		t.Fatalf("RunningPod = %q, %v", name, err)
	}
	if _, err := RunningPod(ctx, kube, "paperless", Ref{"DaemonSet", "x"}); err == nil {
		t.Fatal("a DaemonSet was accepted")
	}
}

// A StatefulSet pod whose claim holds only derived state (Woodpecker's agent id) starts again on a new claim: the
// claim and the pod go, the pod once more after the old claim is gone (the StatefulSet may have remade it on the
// terminating claim), and the other claims stay
func TestResetClaims(t *testing.T) {
	ctx := context.Background()
	claim := func(name string) *corev1.PersistentVolumeClaim {
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "woodpecker", Name: name, UID: types.UID("uid-" + name)}}
	}
	pod := func(name, claimName string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "woodpecker", Name: name},
			Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "c", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName}}}}}}
	}
	kube := fake.NewClientset(claim("agent-config-woodpecker-agent-0"), claim("data-woodpecker-server-0"),
		pod("woodpecker-agent-0", "agent-config-woodpecker-agent-0"), pod("woodpecker-server-0", "data-woodpecker-server-0"))
	c := &Client{Kube: kube, Poll: time.Millisecond, Wait: time.Second}
	if err := c.ResetClaims(ctx, "woodpecker", []string{"agent-config-woodpecker-agent-0", "agent-config-woodpecker-agent-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := kube.CoreV1().PersistentVolumeClaims("woodpecker").Get(ctx, "agent-config-woodpecker-agent-0", metav1.GetOptions{}); err == nil {
		t.Fatal("the agent's claim is still there")
	}
	if _, err := kube.CoreV1().Pods("woodpecker").Get(ctx, "woodpecker-agent-0", metav1.GetOptions{}); err == nil {
		t.Fatal("the agent's pod is still there")
	}
	for _, kept := range []string{"data-woodpecker-server-0"} {
		if _, err := kube.CoreV1().PersistentVolumeClaims("woodpecker").Get(ctx, kept, metav1.GetOptions{}); err != nil {
			t.Fatalf("%s went: %v", kept, err)
		}
	}
	if _, err := kube.CoreV1().Pods("woodpecker").Get(ctx, "woodpecker-server-0", metav1.GetOptions{}); err != nil {
		t.Fatalf("the server's pod went: %v", err)
	}
}
