package rotate

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/duongle-steven/homelab/cli/internal/workload"
)

func kanidmClient(ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("kaniop.rs/v1beta1")
	u.SetKind("KanidmOAuth2Client")
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func fastWorkloads(kube *fake.Clientset) *workload.Client {
	return &workload.Client{Kube: kube, Poll: time.Millisecond, Wait: 2 * time.Second}
}

// kaniop resets the client's secret in Kanidm once the KanidmOAuth2Client carries its annotation, then rewrites the
// app's Secret: the readers restart only after that (a login fails in between: class 2)
func TestKaniopClientRestartsReaders(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "kanidm-client.grafana")
	kube := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "grafana", Name: "grafana-kanidm-oauth2-credentials", ResourceVersion: "1"}})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{KanidmOAuth2Clients: "KanidmOAuth2ClientList"}, kanidmClient("grafana", "grafana"))
	// kaniop: the annotation makes it rewrite the Secret, then it removes the annotation
	dyn.PrependReactor("patch", "kanidmoauth2clients", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.PatchAction).GetPatchType() != types.MergePatchType || !strings.Contains(string(a.(k8stesting.PatchAction).GetPatch()), "kaniop.rs/force-secret-rotation") {
			return false, nil, nil
		}
		go func() {
			time.Sleep(10 * time.Millisecond)
			s, _ := kube.CoreV1().Secrets("grafana").Get(ctx, "grafana-kanidm-oauth2-credentials", metav1.GetOptions{})
			s.ResourceVersion = "2"
			_, _ = kube.CoreV1().Secrets("grafana").Update(ctx, s, metav1.UpdateOptions{})
		}()
		return false, nil, nil
	})
	log := &[]string{}
	kv := newKV(&[]string{}, nil)
	m := KaniopClient{Kube: kube, Dyn: dyn, Store: kv, Workloads: fakeReaders{log: log}, Waits: fastWorkloads(kube)}
	if err := m.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	if kv.data[RecordPath(e)]["secret-version"] != "1" {
		t.Fatalf("state %v", kv.data[RecordPath(e)])
	}
	// A rerun once the Secret changed annotates nothing again
	patches := len(dyn.Actions())
	if err := m.Prepare(ctx, e); err != nil {
		t.Fatal(err)
	}
	if len(dyn.Actions()) != patches {
		t.Fatalf("a rerun asked kaniop for another secret: %v", dyn.Actions()[patches:])
	}
	if err := m.Switch(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(ctx, e); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*log, []string{"restart grafana/grafana", "wait grafana/grafana"}) {
		t.Fatalf("readers: %v", *log)
	}
	if err := m.Revoke(ctx, e); err != nil {
		t.Fatal(err)
	}
	if kv.data[RecordPath(e)]["secret-version"] != "" {
		t.Fatalf("the rotation's state outlived it: %v", kv.data[RecordPath(e)])
	}
}

// kaniop never rewriting the Secret (it is down, or the client is public) fails the rotation naming the Secret
func TestKaniopClientTimesOut(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "kanidm-client.grafana")
	kube := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "grafana", Name: "grafana-kanidm-oauth2-credentials", ResourceVersion: "1"}})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{KanidmOAuth2Clients: "KanidmOAuth2ClientList"}, kanidmClient("grafana", "grafana"))
	waits := fastWorkloads(kube)
	waits.Wait = 50 * time.Millisecond
	m := KaniopClient{Kube: kube, Dyn: dyn, Store: newKV(&[]string{}, nil), Workloads: fakeReaders{log: &[]string{}}, Waits: waits}
	if err := m.Prepare(ctx, e); err == nil || !strings.Contains(err.Error(), "grafana-kanidm-oauth2-credentials") {
		t.Fatalf("Prepare = %v", err)
	}
}

// kaniop writes new admin passwords when their Secret is deleted
func TestKanidmAdminWaitsForRecreate(t *testing.T) {
	ctx := context.Background()
	e := catalogEntry(t, "kanidm.admin-passwords")
	kube := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "kanidm", Name: "kanidm-admin-passwords", UID: "old"}})
	kube.PrependReactor("delete", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		go func() {
			time.Sleep(10 * time.Millisecond)
			_, _ = kube.CoreV1().Secrets("kanidm").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "kanidm", Name: "kanidm-admin-passwords", UID: "new"}}, metav1.CreateOptions{})
		}()
		return false, nil, nil
	})
	kv := newKV(&[]string{}, nil)
	m := KanidmAdmin{Kube: kube, Store: kv, Waits: fastWorkloads(kube)}
	for range 2 {
		if err := m.Prepare(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	deletes := 0
	for _, a := range kube.Actions() {
		if a.GetVerb() == "delete" {
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatalf("deleted the Secret %d times: a rerun must not delete kaniop's new one", deletes)
	}
	for _, phase := range []func(context.Context, Entry) error{m.Switch, m.Verify, m.Revoke} {
		if err := phase(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
}
