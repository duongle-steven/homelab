package test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// Every path staging's gate names matches a tracked file: a renamed directory would otherwise drop out of the gate
// unseen (lab infra forgejo verify, infra/staging/release-paths.yaml)
func TestReleasePathsExist(t *testing.T) {
	raw, err := os.ReadFile("../infra/staging/release-paths.yaml")
	require.NoError(t, err)
	var release struct {
		Paths []string `json:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &release))
	require.NotEmpty(t, release.Paths)
	for _, glob := range release.Paths {
		out, err := exec.Command("git", "-C", "..", "ls-files", "--", ":(glob)"+glob).Output()
		require.NoError(t, err, glob)
		assert.NotEmpty(t, strings.TrimSpace(string(out)), "%s matches no tracked file", glob)
	}
}
