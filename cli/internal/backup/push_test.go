package backup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

func pvc(ns, name, group string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"homelab/backup": group}}}
}

func summary(t *testing.T, used map[string]int64) []byte {
	t.Helper()
	type volume struct {
		UsedBytes int64 `json:"usedBytes"`
		PVCRef    struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"pvcRef"`
	}
	var vols []volume
	for key, bytes := range used {
		ns, name, _ := strings.Cut(key, "/")
		v := volume{UsedBytes: bytes}
		v.PVCRef.Name, v.PVCRef.Namespace = name, ns
		vols = append(vols, v)
	}
	data, err := json.Marshal(map[string]any{"pods": []any{map[string]any{"volume": vols}}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestVolumeBytesSumsTheAppsLabelledClaims(t *testing.T) {
	c, _ := fakeClient()
	ctx := context.Background()
	for _, p := range []*corev1.PersistentVolumeClaim{pvc("paperless", "paperless-data", "manual"), pvc("paperless", "cache", "none"), pvc("ntfy", "ntfy", "daily")} {
		if _, err := c.Kube.CoreV1().PersistentVolumeClaims(p.Namespace).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Kube.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.StatsSummary = func(_ context.Context, node string) ([]byte, error) {
		return summary(t, map[string]int64{"paperless/paperless-data": 1_200_000_000, "paperless/cache": 9_000_000_000, "ntfy/ntfy": 5}), nil
	}
	got, err := c.VolumeBytes(ctx, "paperless")
	if err != nil || got != 1_200_000_000 {
		t.Fatalf("VolumeBytes = %d, %v", got, err)
	}
}

func TestPushRefusesReadOnly(t *testing.T) {
	c, dyn := fakeClient(location("ReadOnly"))
	if _, err := c.Push(context.Background(), "ntfy", time.Second); err == nil || !strings.Contains(err.Error(), "lab backup enable") {
		t.Fatalf("err = %v", err)
	}
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "create" {
			t.Fatal("a backup was created while read-only")
		}
	}
}

func TestPushCreatesLabelledBackup(t *testing.T) {
	c, dyn := fakeClient(location("ReadWrite"))
	c.Now = func() time.Time { return t0 }
	// Velero answers by completing the backup
	dyn.PrependReactor("create", "backups", func(action k8stesting.Action) (bool, runtime.Object, error) {
		u := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		_ = unstructured.SetNestedField(u.Object, "Completed", "status", "phase")
		return false, nil, nil
	})
	run, err := c.Push(context.Background(), "ntfy", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if run.Name != "homelab-ntfy-202610220200" || run.App != "ntfy" || run.Phase != "Completed" {
		t.Fatalf("run = %+v", run)
	}
	b, err := dyn.Resource(Backups).Namespace(Namespace).Get(context.Background(), run.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b.GetLabels()[AppLabel] != "ntfy" {
		t.Fatalf("labels = %v", b.GetLabels())
	}
	ns, _, _ := unstructured.NestedStringSlice(b.Object, "spec", "includedNamespaces")
	kinds, _, _ := unstructured.NestedStringSlice(b.Object, "spec", "includedResources")
	moved, _, _ := unstructured.NestedBool(b.Object, "spec", "snapshotMoveData")
	values, _, _ := unstructured.NestedSlice(b.Object, "spec", "labelSelector", "matchExpressions")
	if strings.Join(ns, ",") != "ntfy" || strings.Join(kinds, ",") != "persistentvolumeclaims,persistentvolumes" || !moved || len(values) != 1 {
		t.Fatalf("spec = %v", b.Object["spec"])
	}
	expr := values[0].(map[string]any)
	if expr["key"] != "homelab/backup" || expr["operator"] != "In" {
		t.Fatalf("selector = %v", expr)
	}
}

func TestPushFailsWhenTheBackupFails(t *testing.T) {
	c, dyn := fakeClient(location("ReadWrite"))
	dyn.PrependReactor("create", "backups", func(action k8stesting.Action) (bool, runtime.Object, error) {
		u := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		_ = unstructured.SetNestedField(u.Object, "PartiallyFailed", "status", "phase")
		return false, nil, nil
	})
	if _, err := c.Push(context.Background(), "ntfy", 5*time.Second); err == nil || !strings.Contains(err.Error(), "PartiallyFailed") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseCredentials(t *testing.T) {
	creds, err := parseCredentials("[default]\naws_access_key_id = id\naws_secret_access_key = secret\nendpoint_url = http://s3.velero:7070\n")
	if err != nil || creds.AccessKey != "id" || creds.SecretKey != "secret" || creds.Endpoint != "http://s3.velero:7070" {
		t.Fatalf("creds = %+v, %v", creds, err)
	}
	if _, err := parseCredentials("[default]\naws_access_key_id = AKIAEXAMPLE\n"); err == nil || strings.Contains(err.Error(), "AKIAEXAMPLE") {
		t.Fatalf("an incomplete file must fail without echoing it: %v", err)
	}
}
