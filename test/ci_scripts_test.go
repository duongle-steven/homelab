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

// choose-values.sh picks the values file the Argo CD bootstrap applies: the seed only on a first install while Forgejo
// does not run, never again once Argo CD reads Forgejo, and never because the cluster could not be asked
func TestArgoCDChooseValues(t *testing.T) {
	const forgejo = "http://forgejo-http.forgejo:3000/ops/homelab"
	for _, c := range []struct {
		name, seed, rootRepo, forgejoPods, want string
		kubectlFails, fails                  bool
	}{
		{"sandbox: no seed", "", "", "", `{"values":"values-dev.yaml","environment":"dev"}`, true, false},
		{"first install, Forgejo not running", "values-seed.yaml", "", "", `{"values":"values-seed.yaml","environment":"seed"}`, false, false},
		{"Forgejo running: switch", "values-seed.yaml", "https://github.com/example/homelab", "pod/forgejo-1", `{"values":"values-dev.yaml","environment":"dev"}`, false, false},
		{"switched, Forgejo restarting: stay", "values-seed.yaml", forgejo, "", `{"values":"values-dev.yaml","environment":"dev"}`, false, false},
		{"cluster unreachable", "values-seed.yaml", "", "", "", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			kubectl := `case "$*" in
  *applicationset*) echo "$ROOT_REPO" ;;
  *pods*) echo "$FORGEJO_PODS" ;;
esac`
			if c.kubectlFails {
				kubectl = `echo "unable to connect" >&2; exit 1`
			}
			_, env := fakeBin(t, map[string]string{"kubectl": kubectl})
			cmd := exec.Command("sh", "../infra/_modules/argocd/choose-values.sh")
			cmd.Env = append(env, "ROOT_REPO="+c.rootRepo, "FORGEJO_PODS="+c.forgejoPods)
			cmd.Stdin = strings.NewReader(`{"values":"values-dev.yaml","environment":"dev","seed":"` + c.seed + `","forgejo_repository":"` + forgejo + `","kubeconfig":"/dev/null"}`)
			out, err := cmd.CombinedOutput()
			if c.fails {
				require.Error(t, err, string(out))
				return
			}
			require.NoError(t, err, string(out))
			assert.JSONEq(t, c.want, string(out))
		})
	}
}
