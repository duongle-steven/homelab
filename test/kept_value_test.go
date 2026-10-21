package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kept-value keeps a value given once (the GitHub App's key) when later applies give none, refuses a different one
// without -replace, and takes it with -replace: the key file is needed only once
func TestKeptValue(t *testing.T) {
	module, err := filepath.Abs("../infra/_modules/kept-value")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`
variable "value" {
  type      = string
  default   = ""
  sensitive = true
}
module "kept" {
  source = "`+module+`"
  value  = var.value
}
output "value" {
  value     = nonsensitive(module.kept.value)
}
`), 0o644))
	tofu := func(args ...string) (string, error) {
		cmd := exec.Command("tofu", append([]string{"-chdir=" + dir}, args...)...)
		cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	apply := func(value string, extra ...string) (string, error) {
		return tofu(append([]string{"apply", "-auto-approve", "-input=false", "-no-color", "-var", "value=" + value}, extra...)...)
	}
	output := func() string {
		out, err := tofu("output", "-raw", "value")
		require.NoError(t, err, out)
		return strings.TrimSpace(out)
	}
	out, err := tofu("init", "-input=false", "-no-color")
	require.NoError(t, err, out)

	out, err = apply("key-a")
	require.NoError(t, err, out)
	assert.Equal(t, "key-a", output())

	out, err = apply("")
	require.NoError(t, err, out)
	assert.Equal(t, "key-a", output(), "an empty value replaced the kept one")

	out, err = apply("key-b")
	assert.Error(t, err, "a different value was taken without -replace:\n%s", out)
	assert.Contains(t, out, "-replace")
	assert.Equal(t, "key-a", output())

	out, err = apply("key-b", "-replace=module.kept.terraform_data.kept")
	require.NoError(t, err, out)
	assert.Equal(t, "key-b", output())
}

// A unit applied before the value existed keeps an empty one: the value given later must fail loudly, not vanish
func TestKeptValueGivenAfterEmpty(t *testing.T) {
	module, err := filepath.Abs("../infra/_modules/kept-value")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`
variable "value" {
  type      = string
  default   = ""
  sensitive = true
}
module "kept" {
  source = "`+module+`"
  value  = var.value
}
`), 0o644))
	run := func(args ...string) (string, error) {
		out, err := exec.Command("tofu", append([]string{"-chdir=" + dir}, args...)...).CombinedOutput()
		return string(out), err
	}
	out, err := run("init", "-input=false", "-no-color")
	require.NoError(t, err, out)
	out, err = run("apply", "-auto-approve", "-input=false", "-no-color")
	require.NoError(t, err, out)
	out, err = run("apply", "-auto-approve", "-input=false", "-no-color", "-var", "value=key-a")
	assert.Error(t, err, "a value given after an empty one was ignored:\n%s", out)
	assert.Contains(t, out, "-replace")
}
