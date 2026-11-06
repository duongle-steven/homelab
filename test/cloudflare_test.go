package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Cloudflare module's edge rules (the rate limit, the check on the WAF's managed rules) plan as intended against a
// mock provider (infra/_modules/cloudflare/tests). A copy runs, so init leaves no lock file or .terraform in the tree
func TestCloudflareEdgeRules(t *testing.T) {
	module, err := filepath.Abs("../infra/_modules/cloudflare")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.CopyFS(dir, os.DirFS(module)))
	tofu := func(args ...string) (string, error) {
		cmd := exec.Command("tofu", append([]string{"-chdir=" + dir}, args...)...)
		cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := tofu("init", "-input=false", "-no-color")
	require.NoError(t, err, out)
	out, err = tofu("test", "-no-color")
	require.NoError(t, err, out)
}
