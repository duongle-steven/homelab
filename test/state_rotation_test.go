package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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
	return stateTreeAt(t, t.TempDir())
}

// stateTreeAt is stateTree in a given directory
func stateTreeAt(t *testing.T, root string) string {
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

// An unencrypted state (an old sandbox, or production before step 8c) is refused, then encrypted by one run with
// HOMELAB_STATE_MIGRATE=1
func TestStateMigrateFromPlain(t *testing.T) {
	root := stateTree(t)
	plain := `{"version":4,"terraform_version":"1.11.0","serial":1,"lineage":"6b9e2a52-0d8b-4c47-9a31-3f1d1f0c0001","outputs":{},"resources":[]}` + "\n"
	path := filepath.Join(root, ".state/sandbox/marker/terraform.tfstate")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(plain), 0o600))

	_, err := tg(t, root, "marker", passphraseA, "", "apply", "-auto-approve")
	require.Error(t, err, "enforced encryption read an unencrypted state")

	migrate := func(args ...string) (string, error) {
		cmd := exec.Command("terragrunt", append(args, "--non-interactive", "--log-level", "error")...)
		cmd.Dir = filepath.Join(root, "infra/sandbox/marker")
		cmd.Env = append(stateEnv(passphraseA, ""), "HOMELAB_STATE_MIGRATE=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := migrate("apply", "-auto-approve")
	require.NoError(t, err, out)
	state := stateFile(t, root, "marker")
	assert.Contains(t, state, "encrypted_data")
	assert.NotContains(t, state, "visible-marker")

	pulled, err := tg(t, root, "marker", passphraseA, "", "state", "pull")
	require.NoError(t, err, pulled)
	assert.Contains(t, pulled, "visible-marker")
}

// A rotation interrupted after some units were rewritten with the new passphrase resumes with the same variables
func TestStateRotationResumes(t *testing.T) {
	root := stateTree(t)
	out, err := tg(t, root, "marker", passphraseA, "", "apply", "-auto-approve")
	require.NoError(t, err, out)
	out, err = tg(t, root, "empty", passphraseB, "", "apply", "-auto-approve")
	require.NoError(t, err, out)

	out, err = rotate(t, root, passphraseB, passphraseA)
	require.NoError(t, err, out)
	for _, unit := range []string{"marker", "empty"} {
		_, err := tg(t, root, unit, passphraseB, "", "state", "pull")
		assert.NoError(t, err, "%s does not read with the new passphrase", unit)
	}
}

// Killed mid-run, the script removes its decrypted copies
func TestStateRotationSignalCleansUp(t *testing.T) {
	root := stateTree(t)
	out, err := tg(t, root, "marker", passphraseA, "", "apply", "-auto-approve")
	require.NoError(t, err, out)

	tmp := t.TempDir()
	cmd := exec.Command("sh", "scripts/rotate-state-passphrase", "sandbox")
	cmd.Dir = root
	cmd.Env = append(stateEnv(passphraseB, passphraseA), "TMPDIR="+tmp)
	require.NoError(t, cmd.Start())
	require.Eventually(t, func() bool {
		entries, _ := filepath.Glob(filepath.Join(tmp, "tmp.*", "*.json"))
		return len(entries) > 0
	}, 60*time.Second, 50*time.Millisecond, "the script never wrote a decrypted copy")
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	_ = cmd.Wait()
	left, _ := filepath.Glob(filepath.Join(tmp, "*"))
	assert.Empty(t, left, "decrypted states left behind")
}

// MIGRATE's unencrypted fallback and PREVIOUS's old-passphrase fallback cannot both apply: refuse the combination
func TestStateMigrateRefusesPrevious(t *testing.T) {
	root := stateTree(t)
	cmd := exec.Command("terragrunt", "plan", "--non-interactive", "--log-level", "error")
	cmd.Dir = filepath.Join(root, "infra/sandbox/marker")
	cmd.Env = append(stateEnv(passphraseB, passphraseA), "HOMELAB_STATE_MIGRATE=1")
	out, err := cmd.CombinedOutput()
	assert.Error(t, err)
	assert.Contains(t, string(out), "HOMELAB_STATE_MIGRATE and HOMELAB_STATE_PASSPHRASE_PREVIOUS")
}
