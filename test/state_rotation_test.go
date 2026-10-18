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

const (
	passphraseA = "passphrase-a-0123456789"
	passphraseB = "passphrase-b-9876543210"
)

// stateTree builds a throwaway repository with this repository's infra/root.hcl and scripts, and two sandbox units:
// "marker" (a terraform_data resource, no provider) and "empty" (no resources).
func stateTree(t *testing.T) string {
	root := t.TempDir()
	mustWrite := func(path, body string, mode os.FileMode) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, path), []byte(body), mode))
	}
	for _, f := range []string{"infra/root.hcl", "scripts/rotate-state-passphrase", "scripts/cloudflare-credentials"} {
		body, err := os.ReadFile(filepath.Join("..", f))
		require.NoError(t, err)
		mustWrite(f, string(body), 0o755)
	}
	mustWrite("infra/_modules/marker/main.tf", `resource "terraform_data" "marker" { input = "visible-marker" }`+"\n", 0o644)
	mustWrite("infra/_modules/empty/main.tf", "\n", 0o644)
	for _, unit := range []string{"marker", "empty"} {
		mustWrite("infra/sandbox/"+unit+"/terragrunt.hcl", `include "root" {
  path = find_in_parent_folders("root.hcl")
}
terraform {
  source = "${find_in_parent_folders("_modules")}//`+unit+`"
}
`, 0o644)
	}
	require.NoError(t, exec.Command("git", "-C", root, "init", "-q").Run())
	return root
}

// tg runs terragrunt in a unit with the given passphrases ("" leaves a variable unset).
func tg(t *testing.T, root, unit, current, previous string, args ...string) (string, error) {
	cmd := exec.Command("terragrunt", append(args, "--non-interactive", "--log-level", "error")...)
	cmd.Dir = filepath.Join(root, "infra/sandbox", unit)
	cmd.Env = stateEnv(current, previous)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func stateEnv(current, previous string) []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HOMELAB_STATE_") && !strings.HasPrefix(e, "TF_VAR_") {
			env = append(env, e)
		}
	}
	if current != "" {
		env = append(env, "HOMELAB_STATE_PASSPHRASE="+current)
	}
	if previous != "" {
		env = append(env, "HOMELAB_STATE_PASSPHRASE_PREVIOUS="+previous)
	}
	return env
}

func rotate(t *testing.T, root, current, previous string) (string, error) {
	cmd := exec.Command("sh", "scripts/rotate-state-passphrase", "sandbox")
	cmd.Dir = root
	cmd.Env = stateEnv(current, previous)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func stateFile(t *testing.T, root, unit string) string {
	body, _ := os.ReadFile(filepath.Join(root, ".state/sandbox", unit, "terraform.tfstate"))
	return string(body)
}

func TestStatePassphraseRotation(t *testing.T) {
	root := stateTree(t)
	out, err := tg(t, root, "marker", passphraseA, "", "apply", "-auto-approve")
	require.NoError(t, err, out)
	_, err = tg(t, root, "empty", passphraseA, "", "apply", "-auto-approve")
	require.NoError(t, err)

	t.Run("state encrypted at rest", func(t *testing.T) {
		state := stateFile(t, root, "marker")
		assert.Contains(t, state, "encrypted_data")
		assert.NotContains(t, state, "visible-marker")
	})

	t.Run("refusals change nothing", func(t *testing.T) {
		before := stateFile(t, root, "marker")
		for _, c := range []struct{ name, current, previous string }{
			{"no previous", passphraseB, ""},
			{"no current", "", passphraseA},
			{"equal", passphraseA, passphraseA},
			{"too short", "short", passphraseA},
		} {
			out, err := rotate(t, root, c.current, c.previous)
			assert.Error(t, err, "%s: %s", c.name, out)
			assert.NotContains(t, out, passphraseA, "a passphrase was printed")
		}
		assert.Equal(t, before, stateFile(t, root, "marker"))
	})

	t.Run("wrong previous changes nothing", func(t *testing.T) {
		before := stateFile(t, root, "marker")
		out, err := rotate(t, root, passphraseB, "wrong-passphrase-000000")
		assert.Error(t, err, out)
		assert.Equal(t, before, stateFile(t, root, "marker"))
	})

	t.Run("rotated", func(t *testing.T) {
		out, err := rotate(t, root, passphraseB, passphraseA)
		require.NoError(t, err, out)
		assert.NotContains(t, out, passphraseA)
		assert.NotContains(t, out, passphraseB)
		pulled, err := tg(t, root, "marker", passphraseB, "", "state", "pull")
		require.NoError(t, err, pulled)
		assert.Contains(t, pulled, "visible-marker")
		_, err = tg(t, root, "marker", passphraseA, "", "state", "pull")
		assert.Error(t, err, "the old passphrase still reads the state")
	})

	t.Run("empty unit skipped", func(t *testing.T) {
		_, err := tg(t, root, "empty", passphraseB, "", "state", "pull")
		assert.NoError(t, err)
	})
}
