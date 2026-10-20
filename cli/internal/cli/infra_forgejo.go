package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/env"
	"github.com/duongle-steven/homelab/cli/internal/forgejo"
	"github.com/duongle-steven/homelab/cli/internal/guard"
	"github.com/duongle-steven/homelab/cli/internal/scope"
	"github.com/duongle-steven/homelab/cli/internal/secrets"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func newInfraForgejoCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "forgejo",
		Short: "Deploy to the GitOps repository ops/homelab on Forgejo",
		Long:  "Production's Argo CD syncs ops/homelab on Forgejo, which changes only through push. --env sandbox targets the sandbox's Forgejo (the tests) and skips the guards.",
	}
	group.PersistentFlags().String("env", "production", "production, or sandbox (the tests)")
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Check that HEAD may be deployed to production",
		Long:  "Pass only when the checkout is origin/master without uncommitted changes, its check checks succeeded, and the pull request merged into master as HEAD has a succeeded check sandbox on its head. A running check or an API error fails.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := forgejoEnv(cmd)
			if err != nil {
				return err
			}
			if e.Name != "production" {
				return fmt.Errorf("verify guards production only")
			}
			head, err := verifyProduction(cmd.Context(), e)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%.12s may be deployed.\n", head)
			return err
		},
	}
	sync := &cobra.Command{
		Use:   "sync",
		Short: "Provision org ops on Forgejo: ops/homelab, deploy and argocd, master's protection, their tokens in OpenBao",
		Long:  "Idempotent: what is right is kept, what is wrong is fixed, what is missing is created. On a first install it waits up to 30 minutes for OpenBao, Forgejo and its admin Secret, then up to 10 more for Argo CD's repository Secret. No secret is printed.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := forgejoEnv(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 35*time.Minute)
			defer cancel()
			// A first install: OpenBao, Forgejo and its admin Secret appear while Argo CD syncs (about 20 minutes)
			syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Minute)
			defer syncCancel()
			var s *session
			if err := retryUntil(syncCtx, 10*time.Second, "OpenBao", func() (err error) {
				s, err = openSession(syncCtx, e)
				return err
			}); err != nil {
				return err
			}
			defer func() { _ = s.close(context.Background()) }()
			var password string
			if err := retryUntil(syncCtx, 10*time.Second, "the Secret forgejo/forgejo-admin", func() error {
				admin, err := s.kube.CoreV1().Secrets("forgejo").Get(syncCtx, "forgejo-admin", metav1.GetOptions{})
				if err != nil {
					return err
				}
				password = string(admin.Data["password"])
				return nil
			}); err != nil {
				return err
			}
			if err := forgejo.Sync(syncCtx, s.ep, password, s.store); err != nil {
				return err
			}
			argocd, err := s.store.Read(ctx, forgejo.ArgoCDPath)
			if err != nil {
				return err
			}
			if err := refreshArgoCDCredential(ctx, s.kube, s.dyn, argocd["token"], 10*time.Minute); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Forgejo's org ops is in sync.")
			return err
		},
	}
	push := &cobra.Command{
		Use:   "push",
		Short: "Deploy: push HEAD to ops/homelab's master as deploy",
		Long:  "Production: only after verify's guards pass, never forced (roll back with a revert through a pull request). Sandbox: a commit with HEAD's tree on top of what the sandbox's Forgejo holds.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := forgejoEnv(cmd)
			if err != nil {
				return err
			}
			var commit string
			if e.Name == "production" {
				if commit, err = verifyProduction(cmd.Context(), e); err != nil {
					return err
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			s, err := openSession(ctx, e)
			if err != nil {
				return err
			}
			defer func() { _ = s.close(context.Background()) }()
			deploy, err := s.store.Read(ctx, forgejo.DeployPath)
			if err != nil {
				return err
			}
			if deploy["token"] == "" {
				return fmt.Errorf("no deploy token in OpenBao (secret/%s): run lab infra forgejo sync first", forgejo.DeployPath)
			}
			if commit == "" {
				if commit, err = forgejo.SandboxCommit(ctx, s.ep, e.Root, deploy["token"]); err != nil {
					return err
				}
			}
			deployed, err := forgejo.Deployed(ctx, s.ep, e.Root, deploy["token"])
			if err != nil {
				return err
			}
			if deployed == commit {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "ops/homelab's master already is %.12s: nothing to push.\n", commit)
				return err
			}
			if err := forgejo.Push(ctx, s.ep, e.Root, deploy["token"], commit); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deployed %.12s to ops/homelab.\n", commit)
			return err
		},
	}
	group.AddCommand(verify, sync, push)
	return group
}

// forgejoEnv loads the environment --env names, from the repository lab runs in.
func forgejoEnv(cmd *cobra.Command) (env.Env, error) {
	name, err := cmd.Flags().GetString("env")
	if err != nil {
		return env.Env{}, err
	}
	root, err := scope.Root()
	if err != nil {
		return env.Env{}, err
	}
	return env.Load(root, name)
}

func verifyProduction(ctx context.Context, e env.Env) (string, error) {
	repository, err := e.GitHubRepository()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return guard.Verify(ctx, e.Root, repository, guard.GitHub{BaseURL: "https://api.github.com", HTTP: &http.Client{Timeout: 30 * time.Second}})
}

var externalSecrets = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}

// session is what sync and push need: the environment, its cluster, OpenBao and Forgejo's endpoint.
type session struct {
	env     env.Env
	kube    kubernetes.Interface
	dyn     dynamic.Interface
	store   *secrets.Store
	ep      forgejo.Endpoint
	cleanup func()
}

func openSession(ctx context.Context, e env.Env) (*session, error) {
	config, err := clientcmd.BuildConfigFromFlags("", e.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", e.Kubeconfig, err)
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	ep, cleanup := forgejo.ProductionEndpoint(e.Domain), func() {}
	if e.Name == "sandbox" {
		if ep, cleanup, err = sandboxEndpoint(ctx, kube, e.Domain); err != nil {
			return nil, err
		}
	}
	store, err := secrets.Open(ctx, config)
	if err != nil {
		cleanup()
		return nil, err
	}
	return &session{env: e, kube: kube, dyn: dyn, store: store, ep: ep, cleanup: cleanup}, nil
}

func (s *session) close(ctx context.Context) error {
	defer s.cleanup()
	return s.store.Close(ctx)
}

// sandboxEndpoint dials the k3d node and trusts the CA trust-manager copies into every namespace.
func sandboxEndpoint(ctx context.Context, kube kubernetes.Interface, domain string) (forgejo.Endpoint, func(), error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", "k3d-homelab-dev-server-0").Output()
	if err != nil {
		return forgejo.Endpoint{}, nil, fmt.Errorf("docker inspect k3d-homelab-dev-server-0: %w (is the sandbox running?)", err)
	}
	ca, err := kube.CoreV1().ConfigMaps("forgejo").Get(ctx, "homelab-dev-ca", metav1.GetOptions{})
	if err != nil {
		return forgejo.Endpoint{}, nil, fmt.Errorf("the sandbox CA (forgejo/homelab-dev-ca): %w", err)
	}
	return forgejo.SandboxEndpoint(domain, strings.TrimSpace(string(out)), []byte(ca.Data["ca.crt"]))
}

// refreshArgoCDCredential makes Argo CD's repository Secret hold token: it force-syncs the ExternalSecret and
// waits, so the bootstrap that follows (make infra) can already switch to Forgejo.
func refreshArgoCDCredential(ctx context.Context, kube kubernetes.Interface, dyn dynamic.Interface, token string, timeout time.Duration) error {
	current := func() (bool, error) {
		secret, err := kube.CoreV1().Secrets("argocd").Get(ctx, "argocd-repo-forgejo", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return string(secret.Data["password"]) == token, nil
	}
	if ok, err := current(); err != nil || ok {
		return err
	}
	deadline := time.Now().Add(timeout)
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{"force-sync": time.Now().Format(time.RFC3339Nano)}}})
	// On a first install Argo CD creates the ExternalSecret only once it has synced argocd-config
	for {
		_, err := dyn.Resource(externalSecrets).Namespace("argocd").Patch(ctx, "argocd-repo-forgejo", types.MergePatchType, patch, metav1.PatchOptions{})
		if err == nil {
			break
		}
		if !apierrors.IsNotFound(err) || time.Now().After(deadline) {
			return fmt.Errorf("force-sync argocd/argocd-repo-forgejo: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	for {
		ok, err := current()
		if err != nil || ok {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("argocd/argocd-repo-forgejo did not get the new token within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// retryUntil calls fn every interval until it succeeds or ctx ends (a first install: the cluster syncs for about
// 20 minutes before OpenBao, Forgejo's admin Secret and Argo CD's ExternalSecret exist).
func retryUntil(ctx context.Context, interval time.Duration, what string, fn func() error) error {
	for {
		err := fn()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w (last error: %v)", what, ctx.Err(), err)
		case <-time.After(interval):
		}
	}
}
