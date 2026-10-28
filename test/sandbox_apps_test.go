package test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GitHub Actions' sandboxes deploy every optional app: the release (dev → master) proves paperless and OpenCloud
// before production does, even though the local sandbox runs them only on request
func TestSandboxWorkflowsAllApps(t *testing.T) {
	for _, workflow := range []string{"sandbox-release", "sandbox-retarget", "sandbox"} {
		raw, err := os.ReadFile("../.github/workflows/" + workflow + ".yaml")
		require.NoError(t, err)
		assert.True(t, strings.Contains(string(raw), "apps=all"), "%s does not pass apps=all to make dev", workflow)
	}
}
