package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/env"
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

func TestRetryUntil(t *testing.T) {
	calls := 0
	err := retryUntil(context.Background(), 10*time.Millisecond, "OpenBao", func() error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("retryUntil() = %v after %d calls", err, calls)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = retryUntil(ctx, 10*time.Millisecond, "OpenBao", func() error { return errors.New("sealed") })
	if err == nil || !strings.Contains(err.Error(), "OpenBao") || !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("retryUntil() after the deadline = %v", err)
	}
}

// On a first install Argo CD creates the ExternalSecret only after its first sync: the refresh waits for it
func TestRefreshArgoCDCredentialWaitsForTheExternalSecret(t *testing.T) {
	kube := fake.NewClientset()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{externalSecrets: "ExternalSecretList"})
	dyn.PrependReactor("patch", "externalsecrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		// A patch before Argo CD created the ExternalSecret is NotFound, as on a real API server
		if _, err := dyn.Tracker().Get(externalSecrets, "argocd", "argocd-repo-forgejo"); err != nil {
			return true, nil, err
		}
		_, err := kube.CoreV1().Secrets("argocd").Create(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-repo-forgejo"},
			Data:       map[string][]byte{"password": []byte("new")},
		}, metav1.CreateOptions{})
		return true, externalSecret(), err
	})
	go func() {
		time.Sleep(1500 * time.Millisecond)
		_, _ = dyn.Resource(externalSecrets).Namespace("argocd").Create(context.Background(), externalSecret(), metav1.CreateOptions{})
	}()
	if err := refreshArgoCDCredential(context.Background(), kube, dyn, "new", 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

// make infra passes the commit it started from to verify and push: a checkout that moved during the apply (a git
// pull) must not deploy a commit other than the one applied
func TestSameCommit(t *testing.T) {
	if err := sameCommit("abc", ""); err != nil {
		t.Fatalf("no --commit: %v", err)
	}
	if err := sameCommit("abc", "abc"); err != nil {
		t.Fatalf("the same commit: %v", err)
	}
	if err := sameCommit("def", "abc"); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("a moved checkout: %v", err)
	}
}

// pushCmd is push's flags around pushCommit, with args set
func pushCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().String("break-glass", "", "")
	cmd.Flags().String("commit", "", "")
	cmd.SetOut(io.Discard)
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// cleanRepo is a checkout with one commit, whose origin cannot be reached (GitHub is down)
func cleanRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "master"},
		{"remote", "add", "origin", "https://unreachable.invalid/homelab.git"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "fix"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(head))
}

// docs/superpowers/specs/2026-10-21-woodpecker-design.md, decision 10
func TestPushCommitBreakGlass(t *testing.T) {
	root, head := cleanRepo(t)
	production, sandbox := env.Env{Name: "production", Root: root}, env.Env{Name: "sandbox", Root: root}
	verified := false
	verify := func(*cobra.Command, env.Env) (string, error) { verified = true; return "verified", nil }

	if got, err := pushCommit(pushCmd(t), production, verify); err != nil || got != "verified" || !verified {
		t.Fatalf("without --break-glass: %q, %v, verified %v", got, err, verified)
	}
	verified = false
	got, err := pushCommit(pushCmd(t, "--break-glass=GitHub outage"), production, verify)
	if err != nil || got != head || verified {
		t.Fatalf("--break-glass: %q, %v, verified %v; want %s without verify", got, err, verified, head)
	}
	if _, err := pushCommit(pushCmd(t, "--break-glass=  "), production, verify); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("an empty reason: %v", err)
	}
	if _, err := pushCommit(pushCmd(t, "--break-glass=x"), sandbox, verify); err == nil || !strings.Contains(err.Error(), "production") {
		t.Fatalf("the sandbox: %v", err)
	}
	if _, err := pushCommit(pushCmd(t, "--break-glass=x", "--commit=0123456789abcdef"), production, verify); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("another commit than make infra's: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "add", "file").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := pushCommit(pushCmd(t, "--break-glass=x"), production, verify); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("a dirty checkout: %v", err)
	}
}
