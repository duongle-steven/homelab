package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/backup"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func fakeBackupClient(t *testing.T, objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	dyn, _ := fakeBackup(t, objects...)
	return dyn
}

func fakeBackup(t *testing.T, objects ...runtime.Object) (*dynamicfake.FakeDynamicClient, *backup.Client) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		backup.Backups:          "BackupList",
		backup.Restores:         "RestoreList",
		backup.Schedules:        "ScheduleList",
		backup.StorageLocations: "BackupStorageLocationList",
	}, objects...)
	client := backup.New(dyn, fake.NewClientset())
	previous := newBackupClient
	newBackupClient = func(*cobra.Command) (*backup.Client, error) { return client, nil }
	t.Cleanup(func() { newBackupClient = previous })
	return dyn, client
}

func veleroLocation(mode string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"accessMode": mode}}}
	u.SetAPIVersion("velero.io/v1")
	u.SetKind("BackupStorageLocation")
	u.SetNamespace(backup.Namespace)
	u.SetName("default")
	return u
}

// runLab runs lab with args, stdin in, and terminal saying whether stdin is a terminal
func runLab(t *testing.T, in string, terminal bool, args ...string) (string, error) {
	t.Helper()
	previous := stdinIsTerminal
	stdinIsTerminal = func(*cobra.Command) bool { return terminal }
	t.Cleanup(func() { stdinIsTerminal = previous })
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(in))
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func accessMode(t *testing.T, dyn *dynamicfake.FakeDynamicClient) string {
	t.Helper()
	bsl, err := dyn.Resource(backup.StorageLocations).Namespace(backup.Namespace).Get(context.Background(), "default", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mode, _, _ := unstructured.NestedString(bsl.Object, "spec", "accessMode")
	return mode
}

func TestEnableRefusesWithoutConfirmation(t *testing.T) {
	dyn := fakeBackupClient(t, veleroLocation("ReadOnly"))
	_, err := runLab(t, "", false, "backup", "enable", "--env", "sandbox")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v, want one naming --yes", err)
	}
	if mode := accessMode(t, dyn); mode != "ReadOnly" {
		t.Fatalf("accessMode = %s after a refusal", mode)
	}
}

func TestEnableAsksAndStopsOnNo(t *testing.T) {
	dyn := fakeBackupClient(t, veleroLocation("ReadOnly"))
	out, err := runLab(t, "n\n", true, "backup", "enable", "--env", "sandbox")
	if err == nil {
		t.Fatal("answering no must fail")
	}
	if !strings.Contains(out, "[y/N]") {
		t.Fatalf("no question asked:\n%s", out)
	}
	if mode := accessMode(t, dyn); mode != "ReadOnly" {
		t.Fatalf("accessMode = %s after no", mode)
	}
}

func TestEnableWithYes(t *testing.T) {
	dyn := fakeBackupClient(t, veleroLocation("ReadOnly"))
	if _, err := runLab(t, "", false, "backup", "enable", "--env", "sandbox", "--yes"); err != nil {
		t.Fatal(err)
	}
	if mode := accessMode(t, dyn); mode != "ReadWrite" {
		t.Fatalf("accessMode = %s", mode)
	}
}

func TestDisableNeedsNoConfirmation(t *testing.T) {
	dyn := fakeBackupClient(t, veleroLocation("ReadWrite"))
	if _, err := runLab(t, "", false, "backup", "disable", "--env", "sandbox"); err != nil {
		t.Fatal(err)
	}
	if mode := accessMode(t, dyn); mode != "ReadOnly" {
		t.Fatalf("accessMode = %s", mode)
	}
}

// sizedClient answers the size queries of lab backup push: the app's volumes 1.2 GB, the bucket 3.0 GB
func sizedClient(t *testing.T) (*dynamicfake.FakeDynamicClient, *backup.Client) {
	t.Helper()
	dyn, c := fakeBackup(t, veleroLocation("ReadWrite"))
	c.Poll = time.Millisecond
	ctx := context.Background()
	if _, err := c.Kube.CoreV1().PersistentVolumeClaims("paperless").Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "paperless", Name: "paperless-data", Labels: map[string]string{"homelab/backup": "manual"}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Kube.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.StatsSummary = func(context.Context, string) ([]byte, error) {
		return []byte(`{"pods":[{"volume":[{"usedBytes":1200000000,"pvcRef":{"name":"paperless-data","namespace":"paperless"}}]}]}`), nil
	}
	if _, err := c.Kube.CoreV1().Secrets(backup.Namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: backup.Namespace, Name: "velero-credentials"},
		Data:       map[string][]byte{"cloud": []byte("[default]\naws_access_key_id = a\naws_secret_access_key = b\nendpoint_url = https://example.r2.cloudflarestorage.com\n")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.ListBucket = func(context.Context, backup.Credentials, string) (int64, error) { return 3_000_000_000, nil }
	return dyn, c
}

func backupsCreated(dyn *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource == "backups" {
			n++
		}
	}
	return n
}

// The person sees the size before group manual reaches R2 (spec decision 7)
func TestPushPrintsSizeAndAsks(t *testing.T) {
	dyn, _ := sizedClient(t)
	out, err := runLab(t, "n\n", true, "backup", "push", "paperless", "--env", "sandbox")
	if err == nil {
		t.Fatal("answering no must fail")
	}
	for _, want := range []string{"volumes 1.2 GB", "bucket 3.0 GB", "after 4.2 GB of 10 GB free", "[y/N]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if n := backupsCreated(dyn); n != 0 {
		t.Fatalf("%d backups created after no", n)
	}
}

func TestPushRefusesWithoutConfirmation(t *testing.T) {
	dyn, _ := sizedClient(t)
	_, err := runLab(t, "", false, "backup", "push", "paperless", "--env", "sandbox")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v, want one naming --yes", err)
	}
	if n := backupsCreated(dyn); n != 0 {
		t.Fatalf("%d backups created without confirmation", n)
	}
}

func TestPushWithYesCreatesTheBackup(t *testing.T) {
	dyn, _ := sizedClient(t)
	dyn.PrependReactor("create", "backups", func(action k8stesting.Action) (bool, runtime.Object, error) {
		u := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		_ = unstructured.SetNestedField(u.Object, "Completed", "status", "phase")
		return false, nil, nil
	})
	out, err := runLab(t, "", false, "backup", "push", "paperless", "--env", "sandbox", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if n := backupsCreated(dyn); n != 1 {
		t.Fatalf("%d backups created", n)
	}
}
