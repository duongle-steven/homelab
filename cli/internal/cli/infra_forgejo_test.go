package cli

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func externalSecret() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("external-secrets.io/v1")
	u.SetKind("ExternalSecret")
	u.SetNamespace("argocd")
	u.SetName("argocd-repo-forgejo")
	return u
}

func TestRefreshArgoCDCredential(t *testing.T) {
	kube := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-repo-forgejo"},
		Data:       map[string][]byte{"password": []byte("old")},
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{externalSecrets: "ExternalSecretList"}, externalSecret())
	// External Secrets answers the force-sync annotation by rewriting the Secret
	dyn.PrependReactor("patch", "externalsecrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		_, err := kube.CoreV1().Secrets("argocd").Update(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-repo-forgejo"},
			Data:       map[string][]byte{"password": []byte("new")},
		}, metav1.UpdateOptions{})
		return true, externalSecret(), err
	})
	if err := refreshArgoCDCredential(context.Background(), kube, dyn, "new", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	patched := false
	for _, a := range dyn.Actions() {
		patched = patched || a.GetVerb() == "patch"
	}
	if !patched {
		t.Fatal("the ExternalSecret was not force-synced")
	}
}

func TestRefreshArgoCDCredentialSkipsACurrentSecret(t *testing.T) {
	kube := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-repo-forgejo"},
		Data:       map[string][]byte{"password": []byte("same")},
	})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{externalSecrets: "ExternalSecretList"}, externalSecret())
	if err := refreshArgoCDCredential(context.Background(), kube, dyn, "same", time.Second); err != nil {
		t.Fatal(err)
	}
	if n := len(dyn.Actions()); n != 0 {
		t.Fatalf("%d calls for a current Secret", n)
	}
}

func TestRefreshArgoCDCredentialTimesOut(t *testing.T) {
	kube := fake.NewClientset()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{externalSecrets: "ExternalSecretList"}, externalSecret())
	if err := refreshArgoCDCredential(context.Background(), kube, dyn, "new", 2*time.Second); err == nil {
		t.Fatal("no error while the Secret never got the token")
	}
}
