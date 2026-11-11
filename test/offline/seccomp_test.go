package offline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The syscalls Buildah's chroot isolation needs beyond RuntimeDefault, each seen refused (seccomp/README.md)
var buildahSeccompAdditions = []string{"mount", "pivot_root", "umount2", "unshare"}

// buildahSeccompBase is the sha256 of the profile without its last entry, re-encoded with sorted keys: k3s 1.37.1's
// containerd RuntimeDefault, unchanged. Regenerating the base (seccomp/README.md) is the only reason to change it
const buildahSeccompBase = "46d672b3640ef25e3278dd04202c0821940e492e92e307edecc8433133291aa3"

// Buildah's Localhost profile (step 15h) is RuntimeDefault plus only what Buildah was seen to need
func TestBuildahSeccompProfile(t *testing.T) {
	dir := filepath.Join(repo.Root(), "infra", "_modules", "metal", "flake", "seccomp")
	data, err := os.ReadFile(filepath.Join(dir, "buildah.json"))
	require.NoError(t, err)
	var profile struct {
		DefaultAction string `json:"defaultAction"`
		Syscalls      []struct {
			Names  []string          `json:"names"`
			Action string            `json:"action"`
			Args   []json.RawMessage `json:"args"`
		} `json:"syscalls"`
	}
	require.NoError(t, json.Unmarshal(data, &profile))
	assert.Equal(t, "SCMP_ACT_ERRNO", profile.DefaultAction)

	// Nothing else is added: the last entry is exactly the additions, and the rest is the base as generated
	require.NotEmpty(t, profile.Syscalls)
	last := profile.Syscalls[len(profile.Syscalls)-1]
	assert.Equal(t, buildahSeccompAdditions, last.Names, "the profile's last entry holds exactly Buildah's additions")
	assert.Equal(t, "SCMP_ACT_ALLOW", last.Action)
	assert.Empty(t, last.Args)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	syscalls := raw["syscalls"].([]any)
	raw["syscalls"] = syscalls[:len(syscalls)-1]
	base, err := json.Marshal(raw)
	require.NoError(t, err)
	sum := sha256.Sum256(base)
	assert.Equal(t, buildahSeccompBase, hex.EncodeToString(sum[:]), "the base changed: regenerate it as seccomp/README.md says")

	// allowed: syscalls allowed whatever their arguments
	allowed := map[string]bool{}
	for _, s := range profile.Syscalls {
		if s.Action == "SCMP_ACT_ALLOW" && len(s.Args) == 0 {
			for _, n := range s.Names {
				allowed[n] = true
			}
		}
	}
	for _, n := range buildahSeccompAdditions {
		assert.True(t, allowed[n], "Buildah needs %s", n)
	}
	// RuntimeDefault's refusals that Buildah does not need stay
	for _, n := range []string{"bpf", "kexec_load", "init_module", "finit_module", "perf_event_open", "keyctl", "add_key",
		"open_by_handle_at", "setns", "clone3", "reboot", "swapon"} {
		assert.False(t, allowed[n], "%s stays refused", n)
	}

	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	require.NoError(t, err)
	assert.Contains(t, string(readme), "k3s v1.37.1+k3s1")
	assert.Contains(t, string(readme), "containerd v2.3.4-k3s1")
	for _, n := range buildahSeccompAdditions {
		assert.Contains(t, string(readme), "`"+n+"`", "README.md says why %s is added", n)
	}
}
