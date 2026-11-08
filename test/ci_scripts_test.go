package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// choose-values.sh picks the values file the Argo CD bootstrap applies: the seed on a first install until Argo CD's
// own credential reads ops/homelab's master (Forgejo runs, lab synced the token and pushed), never again once Argo CD
// reads Forgejo, and never because the cluster could not be asked
func TestArgoCDChooseValues(t *testing.T) {
	const forgejo = "http://forgejo-http.forgejo:3000/ops/homelab"
	const seedValues = `{"values":"values-seed.yaml","environment":"seed"}`
	const forgejoValues = `{"values":"values-dev.yaml","environment":"dev"}`
	for _, c := range []struct {
		name, seed, rootRepo, token, master, want string
		kubectlFails, execFails, fails, noArgoCD  bool
	}{
		// A new cluster has no Argo CD yet: no ApplicationSet kind to ask about is a first install, not a failure
		{name: "first install, no Argo CD CRDs", seed: "values-seed.yaml", noArgoCD: true, want: seedValues},
		{name: "sandbox: no seed", want: forgejoValues, kubectlFails: true},
		{name: "first install, no credential yet", seed: "values-seed.yaml", want: seedValues},
		{name: "credential, nothing pushed", seed: "values-seed.yaml", token: "t1", want: seedValues},
		{name: "credential reads master: switch", seed: "values-seed.yaml", rootRepo: "https://github.com/example/homelab", token: "t1", master: "t1", want: forgejoValues},
		{name: "wrong credential", seed: "values-seed.yaml", token: "t1", master: "t2", want: seedValues},
		// Review Focus 5: the Secret exists but Forgejo's pod is down
		{name: "Forgejo down", seed: "values-seed.yaml", token: "t1", master: "t1", execFails: true, want: seedValues},
		{name: "switched, Forgejo down: stay", seed: "values-seed.yaml", rootRepo: forgejo, execFails: true, want: forgejoValues},
		{name: "cluster unreachable", seed: "values-seed.yaml", kubectlFails: true, fails: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			kubectl := `case "$*" in
  *applicationset*) [ -z "$NO_ARGOCD" ] || { echo 'error: the server doesn'"'"'t have a resource type "applicationset"' >&2; exit 1; }; echo "$ROOT_REPO" ;;
  *"get secret argocd-repo-forgejo"*) [ -z "$TOKEN" ] || printf '%s' "$TOKEN" | base64 ;;
  *exec*) echo "$*" >> "$FIXTURES/exec-args"; [ -z "$EXEC_FAILS" ] || exit 1; [ -n "$MASTER" ] && [ "$(cat)" = "Authorization: token $MASTER" ] ;;
  *) echo "unexpected kubectl $*" >&2; exit 2 ;;
esac`
			if c.kubectlFails {
				kubectl = `echo "unable to connect" >&2; exit 1`
			}
			dir, env := fakeBin(t, map[string]string{"kubectl": kubectl})
			cmd := exec.Command("sh", "../infra/_modules/argocd/choose-values.sh")
			cmd.Env = append(env, "ROOT_REPO="+c.rootRepo, "TOKEN="+c.token, "MASTER="+c.master)
			if c.execFails {
				cmd.Env = append(cmd.Env, "EXEC_FAILS=1")
			}
			if c.noArgoCD {
				cmd.Env = append(cmd.Env, "NO_ARGOCD=1")
			}
			cmd.Stdin = strings.NewReader(`{"values":"values-dev.yaml","environment":"dev","seed":"` + c.seed + `","forgejo_repository":"` + forgejo + `","kubeconfig":"/dev/null"}`)
			out, err := cmd.CombinedOutput()
			if c.fails {
				require.Error(t, err, string(out))
				return
			}
			require.NoError(t, err, string(out))
			assert.JSONEq(t, c.want, string(out))
			// The token reaches curl as a header on stdin, never as an argument (kubectl's or curl's)
			args, _ := os.ReadFile(filepath.Join(dir, "exec-args"))
			if c.token != "" {
				assert.NotContains(t, string(args), c.token)
			}
		})
	}
}

// just dev tests the checkout: with --revision <sha> (GitHub Actions) too, it refuses uncommitted changes and a revision
// other than HEAD, because the tests and their scope come from the checkout
func TestJustDevRevision(t *testing.T) {
	justfile, err := filepath.Abs("../justfile")
	require.NoError(t, err)
	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "file"), []byte("1"), 0o644))
	git("add", "file")
	git("commit", "-q", "-m", "one")
	first := git("rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "file"), []byte("2"), 0o644))
	git("commit", "-q", "-am", "two")
	head := git("rev-parse", "HEAD")
	clean := func(revision string) error {
		out, err := exec.Command("just", "--justfile", justfile, "--working-directory", repo, "sandbox-clean", revision).CombinedOutput()
		t.Log(string(out))
		return err
	}
	assert.NoError(t, clean(""), "no revision, clean tree")
	assert.NoError(t, clean(head), "revision is HEAD")
	assert.NoError(t, clean(head[:12]), "short revision of HEAD")
	assert.Error(t, clean(first), "revision is not HEAD")
	assert.Error(t, clean("does-not-exist"), "unknown revision")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "file"), []byte("3"), 0o644))
	assert.Error(t, clean(head), "uncommitted change")
	assert.Error(t, clean(""), "uncommitted change, no revision")
}

// just infra deploy production deploys in one command: the guards, the infrastructure, Forgejo's org ops, the push,
// then the Argo CD bootstrap again (the switch to Forgejo on a first install); --force applies the infrastructure only
func TestJustInfraDeploy(t *testing.T) {
	stubs := map[string]string{
		"lab":        `echo "lab $*" >> "$FIXTURES/calls"; [ "$3" != verify ] || [ -z "$VERIFY_FAILS" ]`,
		"terragrunt": `echo "terragrunt $(basename "$PWD") $*${HOMELAB_GITHUB_APP_KEY:+ key=$HOMELAB_GITHUB_APP_KEY}" >> "$FIXTURES/calls"`,
		"go":         `exit 0`,
	}
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	commit := "--commit " + strings.TrimSpace(string(head))
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	for _, c := range []struct {
		name string
		opts []string
		env  []string
		want []string
	}{
		{"deploy", nil, nil, []string{
			"lab infra forgejo verify " + commit,
			"terragrunt production run --all apply --non-interactive -- -auto-approve",
			"lab infra forgejo sync",
			"lab infra forgejo push " + commit,
			"terragrunt argocd apply --non-interactive -- -auto-approve",
			"lab secrets list --due --workstation",
		}},
		{"--force", []string{"--force"}, nil, []string{
			"terragrunt production run --all apply --non-interactive -- -auto-approve",
		}},
		{"guards fail", nil, []string{"VERIFY_FAILS=1"}, []string{"lab infra forgejo verify " + commit}},
		// GitHub is down (docs/how-to-guides/deploy-to-forgejo.md): verify checks Forgejo's master before the apply,
		// push says why; the reason's quotes stay out of the shell
		{"break-glass", []string{"--break-glass", `GitHub "outage"; true`}, nil, []string{
			"lab infra forgejo verify " + commit + ` --break-glass=GitHub "outage"; true`,
			"terragrunt production run --all apply --non-interactive -- -auto-approve",
			"lab infra forgejo sync",
			"lab infra forgejo push " + commit + ` --break-glass=GitHub "outage"; true`,
			"terragrunt argocd apply --non-interactive -- -auto-approve",
			"lab secrets list --due --workstation",
		}},
		{"break-glass reason with a dollar sign", []string{"--break-glass", `$HOME is down`}, nil, []string{
			"lab infra forgejo verify " + commit + ` --break-glass=$HOME is down`,
			"terragrunt production run --all apply --non-interactive -- -auto-approve",
			"lab infra forgejo sync",
			"lab infra forgejo push " + commit + ` --break-glass=$HOME is down`,
			"terragrunt argocd apply --non-interactive -- -auto-approve",
			"lab secrets list --due --workstation",
		}},
		{"break-glass with --force", []string{"--force", "--break-glass", "outage"}, nil, []string{""}},
		{"break-glass refused by verify", []string{"--break-glass", "outage"}, []string{"VERIFY_FAILS=1"}, []string{
			"lab infra forgejo verify " + commit + " --break-glass=outage",
		}},
		// A relative key path names a file where just runs, not in the unit's directory
		{"relative key path", []string{"--force"}, []string{"HOMELAB_GITHUB_APP_KEY=keys/app.pem"}, []string{
			"terragrunt production run --all apply --non-interactive -- -auto-approve key=" + root + "/keys/app.pem",
		}},
		{"quoted ~/ key path", []string{"--force"}, []string{"HOMELAB_GITHUB_APP_KEY=~/keys/app.pem", "HOME=/home/someone"}, []string{
			"terragrunt production run --all apply --non-interactive -- -auto-approve key=/home/someone/keys/app.pem",
		}},
		{"key path with a space", []string{"--force"}, []string{"HOMELAB_GITHUB_APP_KEY=my keys/app.pem"}, []string{
			"terragrunt production run --all apply --non-interactive -- -auto-approve key=" + root + "/my keys/app.pem",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, env := fakeBin(t, stubs)
			cmd := exec.Command("just", append(append([]string{"infra", "deploy"}, c.opts...), "production")...)
			cmd.Dir = root
			cmd.Env = append(env, append([]string{"HOMELAB_LAB=" + filepath.Join(dir, "lab"), "HOMELAB_TERRAGRUNT=" + filepath.Join(dir, "terragrunt")}, c.env...)...)
			out, _ := cmd.CombinedOutput()
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			assert.Equal(t, c.want, strings.Split(strings.TrimSpace(string(calls)), "\n"), string(out))
		})
	}
}

// The wait for a namespace outlasts the API server's restarts and the VIP moving (a first sync on three nodes): a
// refused connection is retried until its deadline, and only then fails
func TestWaitNamespaceRetries(t *testing.T) {
	for _, module := range []string{"secrets", "openbao"} {
		t.Run(module, func(t *testing.T) {
			script := "../infra/_modules/" + module + "/wait-namespace.sh"
			dir, env := fakeBin(t, map[string]string{
				"kubectl": `n=$(cat "$FIXTURES/tries" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$FIXTURES/tries"
[ "$n" -gt 2 ] || { echo "connection refused" >&2; exit 1; }`,
			})
			cmd := exec.Command("sh", script)
			cmd.Env = append(env, "KUBECONFIG=/dev/null", "NAMESPACE=x", "WAIT_INTERVAL=0")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
			tries, _ := os.ReadFile(filepath.Join(dir, "tries"))
			assert.Equal(t, "3\n", string(tries))

			_, env = fakeBin(t, map[string]string{"kubectl": `exit 1`})
			cmd = exec.Command("sh", script)
			cmd.Env = append(env, "KUBECONFIG=/dev/null", "NAMESPACE=x", "WAIT_INTERVAL=0", "WAIT_SECONDS=0")
			out, err = cmd.CombinedOutput()
			require.Error(t, err)
			assert.Contains(t, string(out), "x did not appear")
		})
	}
}

// A first sync on testbed's small nodes renders two charts at a time: every helm at once starved k3s of CPU and memory
// until it lost its leases
func TestTestbedArgoCDLimitsTheFirstSync(t *testing.T) {
	out, err := exec.Command("yq", `.argo-cd.configs.params["reposerver.parallelism.limit"]`, "../system/argocd/values-testbed.yaml").Output()
	require.NoError(t, err)
	assert.Equal(t, "2", strings.TrimSpace(string(out)))
	// Waiting for those renders outlasts the default 60 s: every Application reported DeadlineExceeded and tried again
	out, err = exec.Command("yq", `.argo-cd.configs.params["controller.repo.server.timeout.seconds"]`, "../system/argocd/values-testbed.yaml").Output()
	require.NoError(t, err)
	assert.Equal(t, "600", strings.TrimSpace(string(out)))
	// A starved repo server failed its 1 s liveness probe and restarted, losing the renders in progress
	out, err = exec.Command("yq", `.argo-cd.repoServer.livenessProbe.timeoutSeconds`, "../system/argocd/values-testbed.yaml").Output()
	require.NoError(t, err)
	assert.Equal(t, "10", strings.TrimSpace(string(out)))
	out, err = exec.Command("yq", `.argo-cd.configs.cm["timeout.reconciliation"]`, "../system/argocd/values-testbed.yaml").Output()
	require.NoError(t, err)
	assert.Equal(t, "600s", strings.TrimSpace(string(out)))
}
