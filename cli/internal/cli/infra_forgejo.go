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
	group.PersistentFlags().String("commit", "", "refuse unless HEAD is this commit (make infra passes the one it applied)")
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Check that HEAD may be deployed to production",
		Long:  "Pass only when the checkout is origin/master without uncommitted changes, its check checks succeeded, the pull request merged into master as HEAD has a succeeded check sandbox on its head, and the environment config.json's release_proof names marked a commit that proved what HEAD changes since what production runs (or --skip-proof gives a reason). A running check or an API error fails. With --break-glass (GitHub down): a clean checkout whose HEAD fast-forwards ops/homelab's master, asking no GitHub.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := forgejoEnv(cmd)
			if err != nil {
				return err
			}
			if e.Name != "production" {
				return fmt.Errorf("verify guards production only")
			}
			if cmd.Flags().Changed("break-glass") {
				return verifyBreakGlass(cmd, e)
			}
			head, err := verifyProduction(cmd, e)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%.12s may be deployed.\n", head)
			return err
		},
	}
	sync := &cobra.Command{
		Use:   "sync",
		Short: "Provision orgs ops and ci on Forgejo: ops/homelab, deploy and argocd, master's protection, their tokens and Woodpecker's OAuth client in OpenBao",
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
			if err := forgejo.Sync(syncCtx, s.ep, password, s.store, "https://ci."+e.Domain+"/authorize"); err != nil {
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
		Long:  "Production: only after verify's guards pass (or, with --break-glass while GitHub is down, from a clean checkout), never forced (roll back with a revert through a pull request). Sandbox: a commit with HEAD's tree on top of what the sandbox's Forgejo holds.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := forgejoEnv(cmd)
			if err != nil {
				return err
			}
			commit, err := pushCommit(cmd, e, verifyProduction)
			if err != nil {
				return err
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
	verify.Flags().String("break-glass", "", "when GitHub is down: instead of GitHub's checks, a clean checkout that fast-forwards ops/homelab's master (docs/how-to-guides/deploy-to-forgejo.md)")
	push.Flags().String("break-glass", "", "production only, when GitHub is down: skip verify's GitHub checks for this reason (docs/how-to-guides/deploy-to-forgejo.md)")
	for _, c := range []*cobra.Command{verify, push} {
		c.Flags().String("skip-proof", "", "release without release_proof's environment proving what it changes, for this reason (recorded as the commit's status; docs/installation/testbed.md)")
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

func verifyProduction(cmd *cobra.Command, e env.Env) (string, error) {
	repository, err := e.GitHubRepository()
	if err != nil {
		return "", err
	}
	expected, err := cmd.Flags().GetString("commit")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	gh := guard.GitHub{BaseURL: "https://api.github.com", HTTP: &http.Client{Timeout: 30 * time.Second}}
	head, err := guard.Verify(ctx, e.Root, repository, gh)
	if err != nil {
		return "", err
	}
	if err := sameCommit(head, expected); err != nil {
		return "", err
	}
	return head, verifyProof(cmd, e, repository, head, gh)
}

// verifyProof refuses a release that changes what production's proving environment runs (config.json's release_proof)
// unless a commit carries that environment's mark (guard.ProofGate), or --skip-proof gives a reason, recorded as the
// release's status (docs/superpowers/specs/2026-11-02-testbed-design.md)
func verifyProof(cmd *cobra.Command, e env.Env, repository, head string, gh guard.GitHub) error {
	proof, err := e.Proof()
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("skip-proof") {
		reason, err := cmd.Flags().GetString("skip-proof")
		if err != nil {
			return err
		}
		if err := guard.SkipProofReason(reason); err != nil {
			return err
		}
		mark := exec.CommandContext(cmd.Context(), "scripts/mark-proof", proof, head, "skipped: "+reason)
		mark.Dir, mark.Stdout, mark.Stderr = e.Root, cmd.ErrOrStderr(), cmd.ErrOrStderr()
		if err := mark.Run(); err != nil {
			return fmt.Errorf("recording --skip-proof's reason on %.12s: %w", head, err)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Without %s's proof of %.12s: %s\n", proof, head, reason)
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
	defer cancel()
	pullHead, err := gh.MergedPullHead(ctx, repository, head)
	if err != nil {
		return err
	}
	deployed := productionDeployed(ctx, cmd, e)
	candidates, err := guard.ProofCandidates(e.Root, deployed, pullHead)
	if err != nil {
		return err
	}
	if err := guard.ProofGate(ctx, e.Root, repository, proof, deployed, head, append([]string{head}, candidates...), gh); err != nil {
		return err
	}
	if proof == "testbed" {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "only the testbed proved this: no production-like environment did")
	}
	return err
}

// productionDeployed is the commit ops/homelab's master holds on production's Forgejo, or "" when it cannot be read
// (a first install has no Forgejo): the proof's gate then counts everything as changed, never less
func productionDeployed(ctx context.Context, cmd *cobra.Command, e env.Env) string {
	s, err := openSession(ctx, e)
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "production's Forgejo is unreachable (%v): every path counts as changed for the proof's gate\n", err)
		return ""
	}
	defer func() { _ = s.close(context.Background()) }()
	deploy, err := s.store.Read(ctx, forgejo.DeployPath)
	if err == nil && deploy["token"] != "" {
		var deployed string
		if deployed, err = forgejo.Deployed(ctx, s.ep, e.Root, deploy["token"]); err == nil {
			return deployed
		}
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "ops/homelab's master is unreadable (%v): every path counts as changed for the proof's gate\n", err)
	return ""
}

// pushCommit is the commit push deploys: in production the one verify passes or, with --break-glass, the clean
// checkout's HEAD without asking GitHub (Push still refuses anything but a fast-forward of ops/homelab's master);
// in the sandbox none (push builds one).
func pushCommit(cmd *cobra.Command, e env.Env, verify func(*cobra.Command, env.Env) (string, error)) (string, error) {
	if !cmd.Flags().Changed("break-glass") {
		if e.Name != "production" {
			return "", nil
		}
		return verify(cmd, e)
	}
	if e.Name != "production" {
		return "", fmt.Errorf("--break-glass is for production only")
	}
	reason, err := cmd.Flags().GetString("break-glass")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reason) == "" {
		return "", fmt.Errorf("--break-glass needs a reason")
	}
	expected, err := cmd.Flags().GetString("commit")
	if err != nil {
		return "", err
	}
	head, err := guard.Clean(e.Root)
	if err != nil {
		return "", err
	}
	if err := sameCommit(head, expected); err != nil {
		return "", err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Break-glass deploy of %.12s without GitHub's checks: %s\nBring this commit to GitHub's master through a pull request into dev (merge commits only) once GitHub is back.\n", head, reason)
	return head, err
}

// verifyBreakGlass is verify while GitHub is down: the clean checkout's HEAD (pushCommit's checks) must fast-forward
// ops/homelab's master, checked before make infra applies anything.
func verifyBreakGlass(cmd *cobra.Command, e env.Env) error {
	head, err := pushCommit(cmd, e, nil)
	if err != nil {
		return err
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
	if err := forgejo.FastForwards(ctx, s.ep, e.Root, deploy["token"], head); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%.12s may be deployed (break-glass).\n", head)
	return err
}

// sameCommit refuses a HEAD other than expected (when given): the checkout moved since make infra started.
func sameCommit(head, expected string) error {
	if expected != "" && head != expected {
		return fmt.Errorf("the checkout moved from %.12s to %.12s since make infra started: run it again", expected, head)
	}
	return nil
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
