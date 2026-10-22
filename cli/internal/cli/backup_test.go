package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/cli/internal/backup"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func fakeBackupClient(t *testing.T, objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
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
	return dyn
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
