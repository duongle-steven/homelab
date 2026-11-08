package render

import (
	"bytes"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/require"
)

// Renders caches each chart's render by directory and env: the tests below query it many times
var Renders sync.Map

// Yq renders a chart (scripts/render-chart, once per directory and env) and runs a yq expression on it
func Yq(t *testing.T, dir, env, expr string) string {
	t.Helper()
	key := dir + " " + env
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
func Loki(t *testing.T, env, expr string) string { return Yq(t, "system/loki", env, expr) }
