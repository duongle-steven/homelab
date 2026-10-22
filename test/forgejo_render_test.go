package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forgejo-db is synced before Forgejo: on a fresh install CloudNativePG's webhook may refuse the Cluster, and a
// sync that applied the Deployment anyway waits forever for it without the Secret forgejo-db-app
func TestForgejoDatabaseBeforeForgejo(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		t.Run("env="+env, func(t *testing.T) {
			wave := func(kind, name string) string {
				cmd := exec.Command("sh", "-c", `scripts/render-chart platform/forgejo $1 | yq -N "$0"`,
					`select(.kind == "`+kind+`" and .metadata.name == "`+name+`") | .metadata.annotations["argocd.argoproj.io/sync-wave"] // "0"`, env)
				cmd.Dir = ".."
				// stdout only: helm may report a chart it pulled on stderr
				var stderr strings.Builder
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				require.NoError(t, err, stderr.String())
				return strings.TrimSpace(string(out))
			}
			assert.Equal(t, "-1", wave("Cluster", "forgejo-db"))
			assert.Equal(t, "0", wave("Deployment", "forgejo"))
		})
	}
}
