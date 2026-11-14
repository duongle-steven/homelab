package offline

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Step 10f replaced Woodpecker with Argo Workflows and Argo Events: only the history names it (docs/superpowers/ and
// CLAUDE.md's roadmap), and two comments in infra/_modules/metal/flake, which gates releases on the testbed, until the
// next real change there (docs/superpowers/deferred-minors.md)
func TestNoWoodpecker(t *testing.T) {
	cmd := exec.Command("git", "grep", "-il", "woodpecker", "--", ".", ":!docs/superpowers/", ":!CLAUDE.md",
		":!infra/_modules/metal/flake/profiles/k3s-server.nix", ":!infra/_modules/metal/flake/seccomp/README.md",
		":!test/offline/no_woodpecker_test.go")
	cmd.Dir = repo.Root()
	out, err := cmd.Output()
	if err != nil {
		// git grep exits 1 when nothing matches
		require.Equal(t, 1, cmd.ProcessState.ExitCode(), string(out))
	}
	assert.Empty(t, strings.TrimSpace(string(out)))
}
