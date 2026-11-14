package offline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The golden path's settings step (platform/ci/files/settings.sh): a repository chooses only context, containerfile and
// image in .ci.yaml; anything else, or a value that leaves the repository or names another image, fails the run; only a
// push to the default branch publishes (pushes, SBOM, signature), never a pull request or another branch
func TestCISettings(t *testing.T) {
	script := filepath.Join(repo.Root(), "platform", "ci", "files", "settings.sh")
	run := func(t *testing.T, ciYAML string, files []string, env ...string) (map[string]string, error) {
		src := t.TempDir()
		out := t.TempDir()
		for _, f := range files {
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(src, f)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(src, f), []byte("FROM scratch\n"), 0o600))
		}
		if ciYAML != "" {
			require.NoError(t, os.WriteFile(filepath.Join(src, ".ci.yaml"), []byte(ciYAML), 0o600))
		}
		cmd := exec.Command("sh", script)
		cmd.Env = append(os.Environ(), append([]string{"SRC=" + src, "OUT=" + out, "REPO=ci/site", "EVENT=push",
			"REF=refs/heads/main", "DEFAULT_BRANCH=main"}, env...)...)
		combined, err := cmd.CombinedOutput()
		if err != nil {
			return nil, &exec.ExitError{Stderr: combined}
		}
		got := map[string]string{}
		for _, k := range []string{"build", "publish", "context", "containerfile", "image"} {
			b, err := os.ReadFile(filepath.Join(out, k))
			require.NoError(t, err, k)
			got[k] = strings.TrimSpace(string(b))
		}
		return got, nil
	}

	got, err := run(t, "", []string{"Dockerfile"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"build": "true", "publish": "true", "context": ".", "containerfile": "Dockerfile", "image": "site"}, got)

	got, err = run(t, "", []string{"Containerfile", "Dockerfile"})
	require.NoError(t, err)
	assert.Equal(t, "Containerfile", got["containerfile"], "Containerfile first")

	got, err = run(t, "context: web\ncontainerfile: web/Containerfile\nimage: site-web\n", []string{"web/Containerfile"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"build": "true", "publish": "true", "context": "web", "containerfile": "web/Containerfile", "image": "site-web"}, got)

	got, err = run(t, "", nil)
	require.NoError(t, err)
	assert.Equal(t, "false", got["build"], "nothing to build is not a failure")

	for name, env := range map[string][]string{
		"pull request":  {"EVENT=pull_request", "REF=feature"},
		"other branch":  {"REF=refs/heads/feature"},
		"tag-like ref":  {"REF=refs/tags/main"},
		"prefix branch": {"REF=refs/heads/mainline"},
	} {
		got, err := run(t, "", []string{"Dockerfile"}, env...)
		require.NoError(t, err, name)
		assert.Equal(t, "false", got["publish"], name)
		assert.Equal(t, "true", got["build"], name)
	}

	for name, ciYAML := range map[string]string{
		"unknown key":         "image: site\nprivileged: true\n",
		"not YAML":            "image: [site\n",
		"not a map":           "- site\n",
		"image path":          "image: ../other\n",
		"image registry":      "image: evil.io/site\n",
		"image upper":         "image: Site\n",
		"context outside":     "context: ../\n",
		"context absolute":    "context: /etc\n",
		"containerfile out":   "containerfile: ../Dockerfile\n",
		"containerfile none":  "containerfile: Missing\n",
		"context not a value": "context: {a: b}\n",
	} {
		_, err := run(t, ciYAML, []string{"Dockerfile"})
		assert.Error(t, err, name)
	}
}
