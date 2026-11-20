// Package render renders a chart the way Argo CD does (scripts/render-chart) and queries it with yq
package render

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/require"
)

// Renders caches each chart's render by directory, env and the environment variables the render reads: the tests
// query each render many times
var Renders sync.Map

// renderEnv are the environment variables scripts/render-chart and scripts/env-values read: a test that sets one
// gets its own render
var renderEnv = []string{"BACKUP_S3_URL", "HOMELAB_RENDER_CRDS"}

// Yq renders a chart (scripts/render-chart, once per directory, env and renderEnv's values) and runs a yq expression
// on it
func Yq(t testing.TB, dir, env, expr string) string {
	t.Helper()
	key := dir + " " + env
	for _, name := range renderEnv {
		key += " " + name + "=" + os.Getenv(name)
	}
	cached, ok := Renders.Load(key)
	if !ok {
		cmd := exec.Command("scripts/render-chart", dir)
		if env != "" {
			cmd.Args = append(cmd.Args, env)
		}
		cmd.Dir = repo.Root()
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		require.NoError(t, err, stderr.String())
		cached, _ = Renders.LoadOrStore(key, out)
	}
	cmd := exec.Command("yq", "-N", expr)
	cmd.Stdin = bytes.NewReader(cached.([]byte))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	return strings.TrimSpace(string(out))
}

// Loki renders system/loki for env ("" or dev) and runs a yq expression on it
func Loki(t testing.TB, env, expr string) string { return Yq(t, "system/loki", env, expr) }
