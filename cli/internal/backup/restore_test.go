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

// indexAll is the first action holding every part
func (r *recorder) indexAll(t *testing.T, parts ...string) int {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
next:
	for i, a := range r.actions {
		for _, p := range parts {
			if !strings.Contains(a, p) {
				continue next
			}
		}
		return i
	}
	t.Fatalf("no action holding %q in:\n%s", parts, strings.Join(r.actions, "\n"))
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
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "ntfy", Name: "ntfy"}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1)}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1}},
		// Rook reads nothing from OpenBao: a restore must not restart Ceph
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "rook-ceph", Name: "rook-ceph-mon-a"}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1)}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ntfy"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "rook-ceph"}},
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
	// Velero completes a restore as soon as it is created, recreating the namespace's claims labelled with its name
	claims := map[string][]corev1.PersistentVolumeClaim{}
	for _, ns := range []string{"ntfy", "openbao"} {
		list, _ := kube.CoreV1().PersistentVolumeClaims(ns).List(context.Background(), metav1.ListOptions{})
		claims[ns] = list.Items
	}
	dyn.PrependReactor("create", "restores", func(a k8stesting.Action) (bool, runtime.Object, error) {
		u := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		_ = unstructured.SetNestedField(u.Object, "Completed", "status", "phase")
		// A run that does not hold the claims: Velero completes all the same
		if backup, _, _ := unstructured.NestedString(u.Object, "spec", "backupName"); backup == "daily-without-volumes" {
			return false, nil, nil
		}
		namespaces, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "includedNamespaces")
		for _, ns := range namespaces {
			for _, p := range claims[ns] {
				p := p.DeepCopy()
				p.ResourceVersion = ""
				p.Labels["velero.io/restore-name"] = u.GetName()
				_, _ = kube.CoreV1().PersistentVolumeClaims(ns).Create(context.Background(), p, metav1.CreateOptions{})
			}
		}
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
	pause := rec.indexAll(t, "patch applications argocd ntfy", `"automated":null`)
	stop := rec.index(t, `patch deployments ntfy ntfy {"spec":{"replicas":0}}`)
	del := rec.index(t, "delete persistentvolumeclaims ntfy ntfy")
	restore := rec.index(t, "create restores velero")
	start := rec.index(t, `patch deployments ntfy ntfy {"spec":{"replicas":1}}`)
	resume := rec.indexAll(t, "patch applications argocd ntfy", `"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}`)
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
		if strings.Contains(a, `"spec":{"syncPolicy":{"automated":{`) {
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

// The snapshot CronJob must not run between the volume's restore and the raft restore: it would replace the restored
// raft.snap with a snapshot of the new, empty OpenBao, which the raft restore would then read
func TestRestoreOpenBaoBeforeItsCronJobsResume(t *testing.T) {
	c, rec, _ := restoreFixture(t)
	if err := c.RestoreApp(context.Background(), "openbao", Offsite, time.Time{}, daily); err != nil {
		t.Fatal(err)
	}
	bao := rec.index(t, "create jobs openbao")
	resume := rec.index(t, `patch cronjobs openbao openbao-snapshot {"spec":{"suspend":false}}`)
	if resume < bao {
		t.Fatalf("the snapshot CronJob resumed (%d) before the raft restore (%d)", resume, bao)
	}
}

// A whole-cluster restore from one app's push would skip OpenBao and every other app, then report success
func TestPlanClusterRefusesPush(t *testing.T) {
	c, _, _ := restoreFixture(t)
	_, err := c.PlanCluster(context.Background(), Run{Name: "homelab-ntfy-202610211500", App: "ntfy", Phase: "Completed", Started: t0.Add(-time.Hour)})
	if err == nil || !strings.Contains(err.Error(), "lab backup restore ntfy") {
		t.Fatalf("err = %v, want one pointing at the one-app restore", err)
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

// A scheduled run holds only daily volumes: a manual app restores from its own newest push (final review)
func TestAppRunPicksWhatHoldsTheApp(t *testing.T) {
	c, _, _ := restoreFixture(t)
	if _, err := c.Kube.CoreV1().PersistentVolumeClaims("paperless").Create(context.Background(), &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "paperless", Name: "paperless-data", Labels: map[string]string{"homelab/backup": "manual"}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	runs := []Run{
		{Name: "daily-new", Schedule: "daily", Phase: "Completed", Started: t0.Add(-time.Hour)},
		{Name: "homelab-paperless-1", App: "paperless", Phase: "Completed", Started: t0.Add(-48 * time.Hour)},
	}
	for app, want := range map[string]string{"paperless": "homelab-paperless-1", "ntfy": "daily-new"} {
		run, err := c.AppRun(context.Background(), runs, app, "")
		if err != nil || run.Name != want {
			t.Fatalf("%s: picked %+v, %v; want %s", app, run, err, want)
		}
	}
	if _, err := c.AppRun(context.Background(), runs[:1], "paperless", ""); err == nil {
		t.Fatal("a manual app with no push must be refused")
	}
}

// Velero reports Completed even when it skipped a claim (one the run does not hold): the app must not start on an empty
// volume that Argo CD recreates (final review)
func TestRestoreFailsWhenAClaimIsNotRestored(t *testing.T) {
	c, rec, _ := restoreFixture(t)
	err := c.RestoreApp(context.Background(), "ntfy", Offsite, time.Time{}, Run{Name: "daily-without-volumes", Schedule: "daily", Phase: "Completed"})
	if err == nil || !strings.Contains(err.Error(), "ntfy/ntfy") || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("err = %v, want one naming the claim and saying ntfy stays paused", err)
	}
	for _, a := range rec.actions {
		if strings.HasPrefix(a, `patch deployments ntfy ntfy {"spec":{"replicas":1}}`) {
			t.Fatal("ntfy was started without its volume")
		}
	}
}

// A sync Argo CD is running when the restore pauses it would recreate the claims: wait for it to end first
func TestRestoreWaitsForARunningSync(t *testing.T) {
	c, _, dyn := restoreFixture(t)
	a, _ := dyn.Resource(Applications).Namespace("argocd").Get(context.Background(), "ntfy", metav1.GetOptions{})
	_ = unstructured.SetNestedField(a.Object, "Running", "status", "operationState", "phase")
	if _, err := dyn.Resource(Applications).Namespace("argocd").Update(context.Background(), a, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Wait = 50 * time.Millisecond
	err := c.RestoreApp(context.Background(), "ntfy", Offsite, time.Time{}, daily)
	if err == nil || !strings.Contains(err.Error(), "sync") {
		t.Fatalf("err = %v, want one about the running sync", err)
	}
	if _, err := c.Kube.CoreV1().PersistentVolumeClaims("ntfy").Get(context.Background(), "ntfy", metav1.GetOptions{}); err != nil {
		t.Fatalf("the claim was deleted under a running sync: %v", err)
	}
}

// Running a failed restore again, as its error says, puts back what the first run changed (final review)
func TestRestoreAgainAfterAFailure(t *testing.T) {
	c, rec, dyn := restoreFixture(t)
	fail := true
	dyn.PrependReactor("create", "restores", func(k8stesting.Action) (bool, runtime.Object, error) {
		if fail {
			return true, nil, fmt.Errorf("velero is down")
		}
		return false, nil, nil
	})
	if err := c.RestoreApp(context.Background(), "ntfy", Offsite, time.Time{}, daily); err == nil {
		t.Fatal("the first run should fail")
	}
	// The first run deleted the claim: it is in the backup, so Velero brings it back on the second
	fail = false
	if err := c.RestoreApp(context.Background(), "ntfy", Offsite, time.Time{}, daily); err != nil {
		t.Fatal(err)
	}
	rec.index(t, `patch deployments ntfy ntfy {"spec":{"replicas":1}}`)
	a, _ := dyn.Resource(Applications).Namespace("argocd").Get(context.Background(), "ntfy", metav1.GetOptions{})
	automated, _, _ := unstructured.NestedMap(a.Object, "spec", "syncPolicy", "automated")
	if automated["prune"] != true || automated["selfHeal"] != true {
		t.Fatalf("automated = %v, want the original back", automated)
	}
	if _, ok := a.GetAnnotations()[restoreState]; ok {
		t.Fatal("the saved state stayed on the Application")
	}
}

func externalSecret(ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("external-secrets.io/v1")
	u.SetKind("ExternalSecret")
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

// Only the readers of a restored secret restart: Ceph's mons and OSDs all at once would stall every volume
func TestRestoreClusterRestartsOnlyReaders(t *testing.T) {
	c, rec, _ := restoreFixture(t, externalSecret("ntfy", "ntfy-users"))
	if err := c.RestoreCluster(context.Background(), Plan{Run: daily, Apps: []string{"ntfy"}}); err != nil {
		t.Fatal(err)
	}
	rec.indexAll(t, "patch externalsecrets ntfy ntfy-users", "force-sync")
	rec.indexAll(t, "patch deployments ntfy ntfy", "restartedAt")
	for _, a := range rec.actions {
		if strings.Contains(a, "rook-ceph") {
			t.Fatalf("Ceph was touched: %s", a)
		}
	}
}

// In production Argo CD reads Forgejo with a token the restore rolls back; it reads again only once the readers
// refresh, so a whole-cluster restore waits for Argo CD at the end, not per app (final review)
func TestRestoreClusterWaitsForArgoCDAtTheEnd(t *testing.T) {
	unknown := application("ntfy")
	_ = unstructured.SetNestedField(unknown.Object, "Unknown", "status", "sync", "status")
	c, rec, _ := restoreFixture(t, externalSecret("ntfy", "ntfy-users"))
	if _, err := c.Dyn.Resource(Applications).Namespace("argocd").Update(context.Background(), unknown, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.Wait = 100 * time.Millisecond
	err := c.RestoreCluster(context.Background(), Plan{Run: daily, Apps: []string{"ntfy"}})
	if err == nil || !strings.Contains(err.Error(), "ntfy") {
		t.Fatalf("err = %v, want the final wait naming ntfy", err)
	}
	rec.indexAll(t, "patch externalsecrets ntfy ntfy-users", "force-sync")
}

// A finished Job's pod still holds its claim (pvc-protection): openbao-snapshot keeps its last pod an hour, and the
// whole-cluster restore waited 30 minutes for openbao-snapshots to go. The finished pods on the claims go first; the
// Job keeps its record.
func TestRestoreDeletesFinishedPodsOnTheClaims(t *testing.T) {
	c, rec, _ := restoreFixture(t)
	mounts := []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "ntfy"}}}}
	for name, phase := range map[string]corev1.PodPhase{"backup-job-done": corev1.PodSucceeded, "backup-job-failed": corev1.PodFailed} {
		if _, err := c.Kube.CoreV1().Pods("ntfy").Create(context.Background(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ntfy", Name: name},
			Spec:       corev1.PodSpec{Volumes: mounts},
			Status:     corev1.PodStatus{Phase: phase},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// A finished pod on another volume stays
	if _, err := c.Kube.CoreV1().Pods("ntfy").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ntfy", Name: "other-job"},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.RestoreApp(context.Background(), "ntfy", Offsite, time.Time{}, daily); err != nil {
		t.Fatal(err)
	}
	del := rec.index(t, "delete persistentvolumeclaims ntfy ntfy")
	for _, name := range []string{"backup-job-done", "backup-job-failed"} {
		if i := rec.index(t, "delete pods ntfy "+name); i > del {
			t.Fatalf("pod %s deleted after the claim (%d > %d)", name, i, del)
		}
	}
	if _, err := c.Kube.CoreV1().Pods("ntfy").Get(context.Background(), "other-job", metav1.GetOptions{}); err != nil {
		t.Fatalf("a pod on no claim was deleted: %v", err)
	}
}

// kaniop runs in Kanidm's namespace: a restore stops it with the other Deployments, so nothing would take Kanidm's
// StatefulSet down after its replicas go to 0 (the whole-cluster restore waited 30 minutes for kanidm's claim). The
// restore scales kaniop's StatefulSets itself; kaniop puts them back from the Kanidm once it runs again.
func TestRestoreStopsKanidmWithoutItsOperator(t *testing.T) {
	kanidm := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"replicaGroups": []any{map[string]any{"name": "default", "replicas": int64(1)}}},
	}}
	kanidm.SetAPIVersion("kaniop.rs/v1beta1")
	kanidm.SetKind("Kanidm")
	kanidm.SetNamespace("ntfy")
	kanidm.SetName("kanidm")
	c, rec, _ := restoreFixture(t, kanidm)
	ctx := context.Background()
	if _, err := c.Kube.AppsV1().StatefulSets("ntfy").Create(ctx, &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ntfy", Name: "kanidm-default", OwnerReferences: []metav1.OwnerReference{{Kind: "Kanidm", Name: "kanidm"}}},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1)},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Kube.CoreV1().Pods("ntfy").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ntfy", Name: "kanidm-default-0"},
		Spec:       corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "ntfy"}}}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// No operator: only scaling the StatefulSet itself takes its pod away
	kube := c.Kube.(*fake.Clientset)
	kube.PrependReactor("patch", "statefulsets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		p := a.(k8stesting.PatchAction)
		if p.GetName() == "kanidm-default" && string(p.GetPatch()) == `{"spec":{"replicas":0}}` {
			_ = kube.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), "ntfy", "kanidm-default-0")
		}
		return false, nil, nil
	})
	if err := c.RestoreApp(ctx, "ntfy", Offsite, time.Time{}, daily); err != nil {
		t.Fatal(err)
	}
	stop := rec.index(t, `patch statefulsets ntfy kanidm-default {"spec":{"replicas":0}}`)
	if i := rec.indexAll(t, "patch kanidms ntfy kanidm", `"replicas":0`); i > stop {
		t.Fatalf("the StatefulSet was scaled before its Kanidm (%d > %d)", i, stop)
	}
	// Started again by kaniop, from the Kanidm: the restore never scales it up
	for _, a := range rec.actions {
		if strings.HasPrefix(a, "patch statefulsets ntfy kanidm-default") && !strings.Contains(a, `"replicas":0`) {
			t.Fatalf("the restore scaled kaniop's StatefulSet up: %s", a)
		}
	}
}

// Woodpecker's agents keep their ids on claims nobody backs up; a restored server database knows other ids, so the
// restore starts the agents again on new claims and they register anew (the whole-cluster restore waited 30
// minutes on an agent crash-looping with "AgentID not found in database")
func TestRestoreResetsWoodpeckersAgentIDs(t *testing.T) {
	c, rec, _ := restoreFixture(t)
	ctx := context.Background()
	agents := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ntfy", Name: "woodpecker-agent"},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1), VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "agent-config"}}}},
	}
	if _, err := c.Kube.AppsV1().StatefulSets("ntfy").Create(ctx, agents, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Kube.CoreV1().PersistentVolumeClaims("ntfy").Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "ntfy", Name: "agent-config-woodpecker-agent-0"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	previous := agentIDs
	agentIDs = map[string]string{"ntfy": "woodpecker-agent"}
	t.Cleanup(func() { agentIDs = previous })
	if err := c.RestoreApp(ctx, "ntfy", Offsite, time.Time{}, daily); err != nil {
		t.Fatal(err)
	}
	rec.index(t, "delete persistentvolumeclaims ntfy agent-config-woodpecker-agent-0")
	if _, err := c.Kube.CoreV1().PersistentVolumeClaims("ntfy").Get(ctx, "ntfy", metav1.GetOptions{}); err != nil {
		t.Fatalf("the restored claim went: %v", err)
	}
}
