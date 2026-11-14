package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/duongle-steven/homelab/test/internal/kube"
	"github.com/duongle-steven/homelab/test/internal/repo"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// lab runs the CLI from cli/ (as the justfile does) and returns its combined output.
func lab(ctx context.Context, args ...string) string {
	GinkgoHelper()
	cmd := exec.CommandContext(ctx, "go", append([]string{"run", "./cmd/lab"}, args...)...)
	cmd.Dir = filepath.Join(repo.Root(), "cli")
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "lab %v:\n%s", args, out)
	return string(out)
}

// forgejoGit runs git against the sandbox's ops/homelab as user with secret (a token or a password), on the node
// and with the sandbox CA, and returns its output and error.
func forgejoGit(ctx context.Context, dir, user, secret string, args ...string) (string, error) {
	GinkgoHelper()
	ca := filepath.Join(GinkgoT().TempDir(), "ca.crt")
	Expect(os.WriteFile(ca, []byte(configMapValue(ctx, "forgejo", "homelab-dev-ca", "ca.crt")), 0o600)).To(Succeed())
	askpass := filepath.Join(GinkgoT().TempDir(), "askpass")
	Expect(os.WriteFile(askpass, []byte("#!/bin/sh\ncase \"$1\" in Username*) echo \"$GIT_USER\" ;; *) echo \"$GIT_SECRET\" ;; esac\n"), 0o700)).To(Succeed())
	full := append([]string{"-C", dir, "-c", "credential.helper=", "-c", "http.sslCAInfo=" + ca,
		"-c", "http.curloptResolve=" + forgejoHost + ":443:" + gatewayAddress()}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="+askpass, "GIT_USER="+user, "GIT_SECRET="+secret,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ForgejoDeploy runs lab infra forgejo sync twice and push, as just infra deploy production does, against the
// sandbox's Forgejo, then checks what production relies on.
var _ = Describe("ForgejoDeploy", Label("sandbox", "testbed", "forgejo", "deploy"), func() {
	It("syncs, pushes and protects ops/homelab", func(ctx SpecContext) {
		waitForApp(ctx, "https://"+forgejoHost+"/api/healthz")
		lab(ctx, "infra", "forgejo", "sync", "--env", labEnv)
		deploy, argocd := baoValue(ctx, "forgejo/deploy", "token"), baoValue(ctx, "argocd/forgejo", "token")
		Expect(deploy).NotTo(BeEmpty())
		Expect(argocd).NotTo(BeEmpty())
		// Org ci's bot (step 10f): its token, and the webhook it recorded
		bot := baoValue(ctx, "ci/forgejo-token", "token")
		Expect(bot).NotTo(BeEmpty())
		Expect(baoValue(ctx, "ci/forgejo-webhook", "header_sha256")).NotTo(BeEmpty())
		lab(ctx, "infra", "forgejo", "sync", "--env", labEnv)
		Expect(baoValue(ctx, "forgejo/deploy", "token") == deploy).To(BeTrue(), "a second sync replaced deploy's token")
		Expect(baoValue(ctx, "argocd/forgejo", "token") == argocd).To(BeTrue(), "a second sync replaced argocd's token")
		Expect(baoValue(ctx, "ci/forgejo-token", "token") == bot).To(BeTrue(), "a second sync replaced ci-bot's token")

		lab(ctx, "infra", "forgejo", "push", "--env", labEnv)
		Expect(lab(ctx, "infra", "forgejo", "push", "--env", labEnv)).To(ContainSubstring("nothing to push"))

		// Forgejo's master has the checkout's tree
		clone := GinkgoT().TempDir()
		out, err := forgejoGit(ctx, clone, "argocd", argocd, "clone", "--quiet", "https://"+forgejoHost+"/ops/homelab.git", ".")
		Expect(err).NotTo(HaveOccurred(), out)
		tree, err := exec.CommandContext(ctx, "git", "-C", clone, "rev-parse", "HEAD^{tree}").Output()
		Expect(err).NotTo(HaveOccurred())
		head, err := exec.CommandContext(ctx, "git", "-C", repo.Root(), "rev-parse", "HEAD^{tree}").Output()
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(tree))).To(Equal(strings.TrimSpace(string(head))))

		// The protection: argocd reads but cannot push; deploy can neither force-push nor delete master
		_, err = forgejoGit(ctx, clone, "argocd", argocd, "commit", "--quiet", "--allow-empty", "-m", "argocd")
		Expect(err).NotTo(HaveOccurred())
		out, err = forgejoGit(ctx, clone, "argocd", argocd, "push", "--quiet", "origin", "HEAD:refs/heads/master")
		Expect(err).To(HaveOccurred(), "argocd pushed to master:\n%s", out)
		orphan, err := forgejoGit(ctx, clone, "deploy", deploy, "commit-tree", "HEAD^{tree}", "-m", "orphan")
		Expect(err).NotTo(HaveOccurred())
		out, err = forgejoGit(ctx, clone, "deploy", deploy, "push", "--quiet", "--force", "origin", strings.TrimSpace(orphan)+":refs/heads/master")
		Expect(err).To(HaveOccurred(), "deploy force-pushed master:\n%s", out)
		out, err = forgejoGit(ctx, clone, "deploy", deploy, "push", "--quiet", "origin", ":refs/heads/master")
		Expect(err).To(HaveOccurred(), "deploy deleted master:\n%s", out)

		// The allowlist itself: a write collaborator outside it pushes a branch but not master
		admin, adminPassword := secretValue(ctx, "forgejo", "forgejo-admin", "username"), secretValue(ctx, "forgejo", "forgejo-admin", "password")
		api := "https://" + forgejoHost + "/api/v1"
		probe, probePassword := "allowlist-probe", fmt.Sprintf("Probe-%d-pw", time.Now().UnixNano())
		never := false
		Expect(getJSON(ctx, "POST", api+"/admin/users", nil, admin, adminPassword, map[string]any{
			"username": probe, "email": probe + "@noreply.invalid", "password": probePassword, "must_change_password": &never,
		}, nil)).To(Equal(http.StatusCreated))
		DeferCleanup(func(ctx SpecContext) {
			getJSON(ctx, "DELETE", api+"/admin/users/"+probe+"?purge=true", nil, admin, adminPassword, nil, nil)
		}, NodeTimeout(time.Minute))
		Expect(getJSON(ctx, "PUT", api+"/repos/ops/homelab/collaborators/"+probe, nil, admin, adminPassword,
			map[string]any{"permission": "write"}, nil)).To(Equal(http.StatusNoContent))
		out, err = forgejoGit(ctx, clone, probe, probePassword, "push", "--quiet", "origin", "HEAD:refs/heads/"+probe)
		Expect(err).NotTo(HaveOccurred(), "a write collaborator could not push a branch:\n%s", out)
		out, err = forgejoGit(ctx, clone, probe, probePassword, "push", "--quiet", "origin", "HEAD:refs/heads/master")
		Expect(err).To(HaveOccurred(), "a write collaborator outside the allowlist pushed master:\n%s", out)
		_, _ = forgejoGit(ctx, clone, probe, probePassword, "push", "--quiet", "origin", ":refs/heads/"+probe)

		// Site admins are refused too (Forgejo's protection applies to them)
		out, err = forgejoGit(ctx, clone, admin, adminPassword, "push", "--quiet", "origin", "HEAD:refs/heads/master")
		Expect(err).To(HaveOccurred(), "%s (a site admin) pushed master:\n%s", admin, out)

		// Argo CD's credential reads master with the read-only token
		repoSecret := waitForSecret(ctx, "argocd", "argocd-repo-forgejo")
		Expect(string(repoSecret["username"])).To(Equal("argocd"))
		Expect(getJSON(ctx, "GET", "https://"+forgejoHost+"/api/v1/repos/ops/homelab/branches/master", nil,
			string(repoSecret["username"]), string(repoSecret["password"]), nil, nil)).To(Equal(http.StatusOK))
	}, SpecTimeout(45*time.Minute))
})

// At every release Argo CD reads ops/homelab on Forgejo, with the argocd user's read-only token and through the mesh's
// allow for its repo server. A temporary Application proves that path; the switch of a whole cluster from the seed happens
// once and stays unproven (docs/superpowers/specs/2026-11-02-testbed-design.md)
var _ = Describe("ForgejoArgoCDSyncs", Label("sandbox", "testbed", "forgejo"), func() {
	It("lets Argo CD sync ops/homelab", func(ctx SpecContext) {
		waitForApp(ctx, "https://"+forgejoHost+"/api/healthz")
		lab(ctx, "infra", "forgejo", "sync", "--env", labEnv)
		lab(ctx, "infra", "forgejo", "push", "--env", labEnv)
		const name = "forgejo-sync-probe"
		remove := func(ctx context.Context) {
			_, _ = kube.Kubectl(ctx, "argocd", "patch", "application", name, "--type", "merge", "-p",
				`{"metadata":{"finalizers":["resources-finalizer.argocd.argoproj.io"]}}`)
			_, _ = kube.Kubectl(ctx, "argocd", "delete", "application", name, "--ignore-not-found", "--timeout=2m")
			_, _ = kube.Kubectl(ctx, "argocd", "delete", "namespace", name, "--ignore-not-found", "--timeout=2m")
		}
		// A failed run may have left it behind
		remove(ctx)
		DeferCleanup(func(ctx SpecContext) { remove(ctx) }, NodeTimeout(5*time.Minute))
		Expect(kube.Apply(ctx, "argocd", fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: %[1]s
  namespace: argocd
spec:
  project: default
  destination: {name: in-cluster, namespace: %[1]s}
  source:
    repoURL: http://forgejo-http.forgejo:3000/ops/homelab
    targetRevision: master
    path: system/secret-store
  syncPolicy:
    automated: {}
    syncOptions: [CreateNamespace=true]
`, name))).To(Succeed())
		clone := GinkgoT().TempDir()
		out, err := forgejoGit(ctx, clone, "argocd", baoValue(ctx, "argocd/forgejo", "token"), "ls-remote", "https://"+forgejoHost+"/ops/homelab.git", "refs/heads/master")
		Expect(err).NotTo(HaveOccurred(), out)
		master := strings.Fields(out)[0]
		var status string
		Eventually(ctx, func() bool {
			status, _ = kube.Kubectl(ctx, "argocd", "get", "application", name, "-o", "jsonpath={.status.sync.status} {.status.sync.revision}")
			return status == "Synced "+master
		}).WithTimeout(5*time.Minute).WithPolling(5*time.Second).Should(BeTrue(), func() string {
			conditions, _ := kube.Kubectl(ctx, "argocd", "get", "application", name, "-o", "jsonpath={.status.conditions}")
			return fmt.Sprintf("Argo CD did not sync ops/homelab's master %s: status %q, conditions %s", master, status, conditions)
		})
	}, SpecTimeout(40*time.Minute))
})
