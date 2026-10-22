package backup

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

// recorder keeps the order of both fake clients' writes
type recorder struct {
	mu      sync.Mutex
	actions []string
}

func (r *recorder) react(client string) k8stesting.ReactionFunc {
	return func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetVerb() == "get" || a.GetVerb() == "list" || a.GetVerb() == "watch" {
			return false, nil, nil
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		name := ""
		switch x := a.(type) {
		case k8stesting.PatchAction:
			name = x.GetName() + " " + string(x.GetPatch())
		case k8stesting.DeleteAction:
			name = x.GetName()
		case k8stesting.CreateAction:
			if u, ok := x.GetObject().(*unstructured.Unstructured); ok {
				name = u.GetName()
			} else if o, ok := x.GetObject().(metav1.Object); ok {
				name = o.GetName()
			}
		case k8stesting.UpdateAction:
			if o, ok := x.GetObject().(metav1.Object); ok {
				name = o.GetName()
			}
		}
		r.actions = append(r.actions, fmt.Sprintf("%s %s %s %s", a.GetVerb(), a.GetResource().Resource, a.GetNamespace(), name))
		return false, nil, nil
	}
}

func (r *recorder) index(t *testing.T, prefix string) int {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, a := range r.actions {
		if strings.HasPrefix(a, prefix) {
			return i
		}
	}
	t.Fatalf("no action %q in:\n%s", prefix, strings.Join(r.actions, "\n"))
	return -1
}

func application(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec":   map[string]any{"syncPolicy": map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true}}},
		"status": map[string]any{"sync": map[string]any{"status": "Synced"}, "health": map[string]any{"status": "Healthy"}},
	}}
	u.SetAPIVersion("argoproj.io/v1alpha1")
	u.SetKind("Application")
	u.SetNamespace("argocd")
	u.SetName(name)
	return u
}

// restoreFixture: app ntfy with Deployment ntfy (1 replica) on claim ntfy (group daily), Velero completing every
// restore
func restoreFixture(t *testing.T, objects ...runtime.Object) (*Client, *recorder, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	objects = append(objects, location("ReadOnly"), application("ntfy"), application("openbao"))
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		Backups:          "BackupList",
		Restores:         "RestoreList",
		Schedules:        "ScheduleList",
		StorageLocations: "BackupStorageLocationList",
		Applications:     "ApplicationList",
		CNPGClusters:     "ClusterList",
		Kanidms:          "KanidmList",
		VolumeSnapshots:  "VolumeSnapshotList",
		ExternalSecrets:  "ExternalSecretList",
	}, objects...)
	quantity := resource.MustParse("1Gi")
	kube := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", CreationTimestamp: metav1.NewTime(t0)}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "ntfy", Name: "ntfy"}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1)}},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ntfy", Name: "ntfy", Labels: map[string]string{"homelab/backup": "daily"}},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				StorageClassName: ptr.To("csi-hostpath"),
				Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: quantity}},
			},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: "openbao", Name: "openbao-snapshots", Labels: map[string]string{"homelab/backup": "daily"}},
		},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "openbao", Name: "openbao-snapshot"},
			Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "snapshot", Image: "quay.io/openbao/openbao:2.7.1"}},
			}}}}},
		},
	)
	rec := &recorder{}
	dyn.PrependReactor("*", "*", rec.react("dyn"))
	kube.PrependReactor("*", "*", rec.react("kube"))
	// Velero completes a restore as soon as it is created
	dyn.PrependReactor("create", "restores", func(a k8stesting.Action) (bool, runtime.Object, error) {
		u := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		_ = unstructured.SetNestedField(u.Object, "Completed", "status", "phase")
		return false, nil, nil
	})
	// The OpenBao restore Job succeeds
	kube.PrependReactor("create", "jobs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		a.(k8stesting.CreateAction).GetObject().(*batchv1.Job).Status.Succeeded = 1
		return false, nil, nil
	})
	c := New(dyn, kube)
	c.Poll = time.Millisecond
	c.Wait = 2 * time.Second
	return c, rec, dyn
}

var daily = Run{Name: "daily-20261021020000", Schedule: "daily", Phase: "Completed", Started: t0.Add(-24 * time.Hour)}

// Argo CD must not undo a restore in progress (plan, Review Focus 3)
func TestRestorePausesSync(t *testing.T) {
	c, rec, _ := restoreFixture(t)
	if err := c.RestoreApp(context.Background(), "ntfy", Offsite, time.Time{}, daily); err != nil {
		t.Fatal(err)
	}
	pause := rec.index(t, `patch applications argocd ntfy {"spec":{"syncPolicy":{"automated":null}}}`)
	stop := rec.index(t, `patch deployments ntfy ntfy {"spec":{"replicas":0}}`)
	del := rec.index(t, "delete persistentvolumeclaims ntfy ntfy")
	restore := rec.index(t, "create restores velero")
	start := rec.index(t, `patch deployments ntfy ntfy {"spec":{"replicas":1}}`)
	resume := rec.index(t, `patch applications argocd ntfy {"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}}`)
	if !(pause < stop && stop < del && del < restore && restore < start && start < resume) {
		t.Fatalf("order pause %d, stop %d, delete %d, restore %d, start %d, resume %d", pause, stop, del, restore, start, resume)
	}
}

func TestRestoreStopsPausedOnError(t *testing.T) {
	c, rec, dyn := restoreFixture(t)
	dyn.PrependReactor("create", "restores", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("velero is down")
	})
	err := c.RestoreApp(context.Background(), "ntfy", Offsite, time.Time{}, daily)
	if err == nil || !strings.Contains(err.Error(), "paused") || !strings.Contains(err.Error(), "ntfy") {
		t.Fatalf("err = %v, want one saying ntfy stays paused", err)
	}
	for _, a := range rec.actions {
		if strings.HasPrefix(a, `patch applications argocd ntfy {"spec":{"syncPolicy":{"automated":{`) {
			t.Fatal("the sync was resumed over a half-restored volume")
		}
	}
}

func TestRestoreClusterOpenBaoFirst(t *testing.T) {
	c, rec, _ := restoreFixture(t)
	plan, err := c.PlanCluster(context.Background(), daily)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Apps) != 2 || plan.Apps[0] != "openbao" || plan.Apps[1] != "ntfy" {
		t.Fatalf("plan = %v", plan.Apps)
	}
	if err := c.RestoreCluster(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	bao := rec.index(t, "create jobs openbao")
	ntfy := rec.index(t, "delete persistentvolumeclaims ntfy ntfy")
	if bao > ntfy {
		t.Fatal("ntfy was restored before OpenBao's snapshot")
	}
}

func TestRestoreClusterRefusesReadWrite(t *testing.T) {
	c, _, _ := restoreFixture(t)
	if _, err := c.Dyn.Resource(StorageLocations).Namespace(Namespace).Patch(context.Background(), "default", "application/merge-patch+json", []byte(`{"spec":{"accessMode":"ReadWrite"}}`), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	err := c.RestoreCluster(context.Background(), Plan{Run: daily, Apps: []string{"ntfy"}})
	if err == nil || !strings.Contains(err.Error(), "lab backup disable") {
		t.Fatalf("err = %v", err)
	}
}

func snapshot(ns, pvc, name string, created time.Time, ready bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"readyToUse": ready}}}
	u.SetAPIVersion("snapshot.storage.k8s.io/v1")
	u.SetKind("VolumeSnapshot")
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetLabels(map[string]string{"homelab/snapshot": "hourly", "homelab/pvc": pvc})
	u.SetCreationTimestamp(metav1.NewTime(created))
	return u
}

func TestRestoreLocalPicksSnapshotAt(t *testing.T) {
	c, _, _ := restoreFixture(t,
		snapshot("ntfy", "ntfy", "ntfy-2026102200", t0.Add(-2*time.Hour), true),
		snapshot("ntfy", "ntfy", "ntfy-2026102201", t0.Add(-time.Hour), true),
		snapshot("ntfy", "ntfy", "ntfy-2026102202", t0, false),
		snapshot("ntfy", "ntfy", "ntfy-2026102203", t0.Add(time.Hour), true),
	)
	if err := c.RestoreApp(context.Background(), "ntfy", Local, t0.Add(30*time.Minute), Run{}); err != nil {
		t.Fatal(err)
	}
	p, err := c.Kube.CoreV1().PersistentVolumeClaims("ntfy").Get(context.Background(), "ntfy", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The newest ready snapshot at or before --at: 01:00 (02:00 is not ready, 03:00 is after)
	if p.Spec.DataSource == nil || p.Spec.DataSource.Name != "ntfy-2026102201" || p.Spec.DataSource.Kind != "VolumeSnapshot" {
		t.Fatalf("dataSource = %+v", p.Spec.DataSource)
	}
	if p.Labels["homelab/backup"] != "daily" || *p.Spec.StorageClassName != "csi-hostpath" {
		t.Fatalf("the claim lost its metadata or spec: %+v", p)
	}
}
