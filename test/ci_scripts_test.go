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

// sandbox-scope decides whether a push to dev runs the sandbox: the whole suite unless every change since the
// previous push is skipped, and never "nothing to test" because the selector failed
func TestCISandboxScope(t *testing.T) {
	const before = "1111111111111111111111111111111111111111"
	for _, c := range []struct {
		name, lab, before, want string
		fails                    bool
	}{
		{"changes to test", `[ "$5" = --alone ] && echo NodeRestart || echo Smoke`, before, "scope=full", false},
		{"every change skipped", `echo`, before, "", false},
		{"only alone tests", `[ "$5" = --alone ] && echo NodeRestart || echo`, before, "scope=full", false},
		{"selector fails", `echo "base does not exist" >&2; exit 1`, before, "", true},
		{"new branch", `echo "must not run" >&2; exit 1`, "0000000000000000000000000000000000000000", "scope=full", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, env := fakeBin(t, map[string]string{"lab": `printf '%s ' "$@" >> "$FIXTURES/calls"; echo >> "$FIXTURES/calls"
` + c.lab})
			out, err := runScript(t, append(env, "SANDBOX_LAB="+filepath.Join(dir, "lab")), "sandbox-scope", c.before)
			if c.fails {
				require.Error(t, err, out)
				return
			}
			require.NoError(t, err, out)
			assert.Equal(t, c.want, lastLine(out))
			if c.before == before {
				calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
				assert.Contains(t, string(calls), "test scope --base "+before)
			}
		})
	}
}

func lastLine(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			return s[i+1:]
		}
	}
	return s
}

// choose-values.sh picks the values file the Argo CD bootstrap applies: the seed on a first install until Argo CD's
// own credential reads ops/homelab's master (Forgejo runs, lab synced the token and pushed), never again once Argo CD
// reads Forgejo, and never because the cluster could not be asked
func TestArgoCDChooseValues(t *testing.T) {
	const forgejo = "http://forgejo-http.forgejo:3000/ops/homelab"
	const seedValues = `{"values":"values-seed.yaml","environment":"seed"}`
	const forgejoValues = `{"values":"values-dev.yaml","environment":"dev"}`
	for _, c := range []struct {
		name, seed, rootRepo, token, master, want string
		kubectlFails, execFails, fails           bool
	}{
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
  *applicationset*) echo "$ROOT_REPO" ;;
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

// make dev tests the checkout: with revision=<sha> (GitHub Actions) too, it refuses uncommitted changes and a revision
// other than HEAD, because the tests and their scope come from the checkout
func TestMakeDevRevision(t *testing.T) {
	makefile, err := filepath.Abs("../Makefile")
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
		out, err := exec.Command("make", "-s", "-f", makefile, "-C", repo, "sandbox-clean", "revision="+revision).CombinedOutput()
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

// make infra env=production deploys in one command: the guards, the infrastructure, Forgejo's org ops, the push, then
// the Argo CD bootstrap again (the switch to Forgejo on a first install); force=1 applies the infrastructure only
func TestMakeInfraProduction(t *testing.T) {
	stubs := map[string]string{
		"lab":        `echo "lab $*" >> "$FIXTURES/calls"; [ "$*" != "infra forgejo verify" ] || [ -z "$VERIFY_FAILS" ]`,
		"terragrunt": `echo "terragrunt $(basename "$PWD") $*" >> "$FIXTURES/calls"`,
		"go":         `exit 0`,
	}
	for _, c := range []struct {
		name string
		vars []string
		env  []string
		want []string
	}{
		{"deploy", nil, nil, []string{
			"lab infra forgejo verify",
			"terragrunt production run --all apply --non-interactive -- -auto-approve",
			"lab infra forgejo sync",
			"lab infra forgejo push",
			"terragrunt argocd apply --non-interactive -- -auto-approve",
		}},
		{"force=1", []string{"force=1"}, nil, []string{
			"terragrunt production run --all apply --non-interactive -- -auto-approve",
		}},
		{"guards fail", nil, []string{"VERIFY_FAILS=1"}, []string{"lab infra forgejo verify"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, env := fakeBin(t, stubs)
			args := append([]string{"-s", "-C", "..", "infra", "env=production", "lab_cli=" + filepath.Join(dir, "lab"), "terragrunt=" + filepath.Join(dir, "terragrunt")}, c.vars...)
			cmd := exec.Command("make", args...)
			cmd.Env = append(env, c.env...)
			out, _ := cmd.CombinedOutput()
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			assert.Equal(t, c.want, strings.Split(strings.TrimSpace(string(calls)), "\n"), string(out))
		})
	}
}
