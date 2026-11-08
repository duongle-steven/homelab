package kube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKubectl puts a kubectl first on PATH that records its arguments and stdin in dir and runs script after.
func fakeKubectl(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	body := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > \"" + dir + "/args\"\ncat > \"" + dir + "/stdin\"\n" + script + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kubectl"), []byte(body), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(body)
}

func TestKubectlNamespace(t *testing.T) {
	dir := fakeKubectl(t, "echo out; echo warn >&2")
	out, err := Kubectl(context.Background(), "x", "get", "pods")
	require.NoError(t, err)
	assert.Equal(t, "--namespace\nx\nget\npods\n", read(t, filepath.Join(dir, "args")))
	assert.Equal(t, "out\nwarn", out)

	_, err = Kubectl(context.Background(), "", "get", "nodes")
	require.NoError(t, err)
	assert.Equal(t, "get\nnodes\n", read(t, filepath.Join(dir, "args")))
}

func TestKubectlFailureCarriesOutput(t *testing.T) {
	fakeKubectl(t, "echo 'not found' >&2; exit 1")
	out, err := Kubectl(context.Background(), "", "get", "pod", "p")
	require.Error(t, err)
	assert.Equal(t, "not found", out)
	assert.Contains(t, err.Error(), "not found")
}

func TestApplyPassesManifestOnStdin(t *testing.T) {
	dir := fakeKubectl(t, "")
	require.NoError(t, Apply(context.Background(), "ns", "kind: ConfigMap\n"))
	assert.Equal(t, "--namespace\nns\napply\n-f\n-\n", read(t, filepath.Join(dir, "args")))
	assert.Equal(t, "kind: ConfigMap\n", read(t, filepath.Join(dir, "stdin")))

	require.NoError(t, Delete(context.Background(), "ns", "kind: ConfigMap\n"))
	assert.Equal(t, "--namespace\nns\ndelete\n-f\n-\n", read(t, filepath.Join(dir, "args")))
}

func TestNamespaces(t *testing.T) {
	dir := fakeKubectl(t, "")
	require.NoError(t, CreateNamespace(context.Background(), "a"))
	assert.Equal(t, "create\nnamespace\na\n", read(t, filepath.Join(dir, "args")))
	require.NoError(t, DeleteNamespace(context.Background(), "a"))
	assert.Equal(t, "delete\nnamespace\na\n--wait=false\n", read(t, filepath.Join(dir, "args")))
}

func TestGetJSONIgnoresStderr(t *testing.T) {
	dir := fakeKubectl(t, `echo 'Warning: deprecated' >&2; echo '{"metadata":{"name":"p"}}'`)
	var pod struct {
		Metadata struct{ Name string } `json:"metadata"`
	}
	require.NoError(t, GetJSON(context.Background(), "ns", &pod, "pod", "p"))
	assert.Equal(t, "p", pod.Metadata.Name)
	assert.Equal(t, "--namespace\nns\nget\npod\np\n--output\njson\n", read(t, filepath.Join(dir, "args")))
}

func TestKubectlStopsWithContext(t *testing.T) {
	fakeKubectl(t, "exec sleep 60")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Kubectl(ctx, "", "get", "pods")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "killed"), err.Error())
}
