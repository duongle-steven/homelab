package offline

import (
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
