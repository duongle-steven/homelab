package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

var t0 = time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)

func veleroObject(kind, name string, fields map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	for k, v := range fields {
		u.Object[k] = v
	}
	u.SetAPIVersion("velero.io/v1")
	u.SetKind(kind)
	u.SetNamespace(Namespace)
	u.SetName(name)
	return u
}

func backupObject(name, schedule string, started time.Time, phase string) *unstructured.Unstructured {
	u := veleroObject("Backup", name, map[string]any{"status": map[string]any{
		"phase":               phase,
		"startTimestamp":      started.Format(time.RFC3339),
		"completionTimestamp": started.Add(5 * time.Minute).Format(time.RFC3339),
	}})
	if schedule != "" {
		u.SetLabels(map[string]string{"velero.io/schedule-name": schedule})
	}
	return u
}

func fakeClient(objects ...runtime.Object) (*Client, *dynamicfake.FakeDynamicClient) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		Backups:          "BackupList",
		Restores:         "RestoreList",
		Schedules:        "ScheduleList",
		StorageLocations: "BackupStorageLocationList",
	}, objects...)
	kube := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", CreationTimestamp: metav1.NewTime(t0)}})
	c := New(dyn, kube)
	c.Poll = 10 * time.Millisecond
	return c, dyn
}

func TestRunsNewestFirst(t *testing.T) {
	c, _ := fakeClient(
		backupObject("daily-1", "daily", t0.Add(-48*time.Hour), "Completed"),
		backupObject("daily-2", "daily", t0.Add(-24*time.Hour), "Completed"),
	)
	runs, err := c.Runs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].Name != "daily-2" || runs[0].Schedule != "daily" || runs[0].Phase != "Completed" {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestClusterCreated(t *testing.T) {
	c, _ := fakeClient()
	created, err := c.ClusterCreated(context.Background())
	if err != nil || !created.Equal(t0) {
		t.Fatalf("created = %v, %v", created, err)
	}
}

// A restore must not pick a backup the new cluster made of its own empty volumes (plan, Review Focus 2)
func TestPickBackupIgnoresOwnBackups(t *testing.T) {
	runs := []Run{
		{Name: "daily-new", Phase: "Completed", Started: t0.Add(time.Hour)},
		{Name: "daily-failed", Phase: "Failed", Started: t0.Add(-24 * time.Hour)},
		{Name: "daily-old", Phase: "Completed", Started: t0.Add(-48 * time.Hour)},
	}
	run, err := PickRun(runs, t0, "")
	if err != nil || run.Name != "daily-old" {
		t.Fatalf("picked %+v, %v", run, err)
	}
}

func TestPickBackupByName(t *testing.T) {
	runs := []Run{{Name: "daily-new", Phase: "Completed", Started: t0.Add(time.Hour)}, {Name: "daily-old", Phase: "Completed", Started: t0.Add(-time.Hour)}}
	run, err := PickRun(runs, t0, "daily-new")
	if err != nil || run.Name != "daily-new" {
		t.Fatalf("picked %+v, %v", run, err)
	}
	if _, err := PickRun(runs, t0, "missing"); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("an unknown name: %v", err)
	}
	if _, err := PickRun([]Run{{Name: "daily-x", Phase: "Failed"}}, t0, "daily-x"); err == nil || !strings.Contains(err.Error(), "Failed") {
		t.Fatalf("a failed backup by name: %v", err)
	}
}

func TestPickBackupNoneBefore(t *testing.T) {
	_, err := PickRun([]Run{{Name: "daily-new", Phase: "Completed", Started: t0.Add(time.Hour)}}, t0, "")
	if err == nil || !strings.Contains(err.Error(), "--backup") {
		t.Fatalf("err = %v, want one naming --backup", err)
	}
}

func location(mode string) *unstructured.Unstructured {
	return veleroObject("BackupStorageLocation", "default", map[string]any{"spec": map[string]any{"accessMode": mode}})
}

func schedule(name string, paused bool) *unstructured.Unstructured {
	return veleroObject("Schedule", name, map[string]any{"spec": map[string]any{"paused": paused}})
}

func TestModeReadOnly(t *testing.T) {
	c, _ := fakeClient(location("ReadOnly"))
	readOnly, err := c.Mode(context.Background())
	if err != nil || !readOnly {
		t.Fatalf("readOnly = %v, %v", readOnly, err)
	}
}

func TestSetModeEnables(t *testing.T) {
	c, dyn := fakeClient(location("ReadOnly"), schedule("daily", true), schedule("weekly", true))
	if err := c.SetMode(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	bsl, _ := dyn.Resource(StorageLocations).Namespace(Namespace).Get(context.Background(), "default", metav1.GetOptions{})
	if mode, _, _ := unstructured.NestedString(bsl.Object, "spec", "accessMode"); mode != "ReadWrite" {
		t.Fatalf("accessMode = %q", mode)
	}
	for _, name := range []string{"daily", "weekly"} {
		s, _ := dyn.Resource(Schedules).Namespace(Namespace).Get(context.Background(), name, metav1.GetOptions{})
		if paused, _, _ := unstructured.NestedBool(s.Object, "spec", "paused"); paused {
			t.Fatalf("schedule %s still paused", name)
		}
	}
}

func TestSetModeDisables(t *testing.T) {
	c, dyn := fakeClient(location("ReadWrite"), schedule("daily", false))
	if err := c.SetMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	bsl, _ := dyn.Resource(StorageLocations).Namespace(Namespace).Get(context.Background(), "default", metav1.GetOptions{})
	if mode, _, _ := unstructured.NestedString(bsl.Object, "spec", "accessMode"); mode != "ReadOnly" {
		t.Fatalf("accessMode = %q", mode)
	}
	s, _ := dyn.Resource(Schedules).Namespace(Namespace).Get(context.Background(), "daily", metav1.GetOptions{})
	if paused, _, _ := unstructured.NestedBool(s.Object, "spec", "paused"); !paused {
		t.Fatal("schedule daily not paused")
	}
}
