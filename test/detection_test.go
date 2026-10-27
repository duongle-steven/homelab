package test

import (
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAWSKey builds an access key id gitleaks detects, at runtime: this repository holds no key-shaped literal
func fakeAWSKey(t *testing.T) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	b := make([]byte, 16)
	_, err := rand.Read(b)
	require.NoError(t, err)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "AKIA" + string(b)
}

// gitRepo makes a repository with one commit per file content given, and returns its path and the commits' hashes
func gitRepo(t *testing.T, files ...map[string]string) (string, []string) {
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	var commits []string
	for i, set := range files {
		for name, content := range set {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
		}
		run("add", "-A")
		run("commit", "-q", "-m", "commit "+string(rune('a'+i)))
		commits = append(commits, run("rev-parse", "HEAD"))
	}
	return dir, commits
}

// runCheck runs one of scripts/check-* in dir with env and returns its output and error
func runCheck(t *testing.T, script, dir string, env ...string) (string, error) {
	abs, err := filepath.Abs("../scripts/" + script)
	require.NoError(t, err)
	cmd := exec.Command(abs)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// leaks fails on a secret the change brings, never on one before BASE, and prints none
func TestCheckLeaks(t *testing.T) {
	key := fakeAWSKey(t)
	dir, commits := gitRepo(t,
		map[string]string{"README.md": "clean\n"},
		map[string]string{"config.env": "AWS_ACCESS_KEY_ID=" + key + "\n"},
		map[string]string{"README.md": "still clean\n"})
	out, err := runCheck(t, "check-leaks", dir, "BASE="+commits[0])
	require.Error(t, err, "a leak in the change passed:\n%s", out)
	assert.NotContains(t, out, key[4:], "the secret was printed")
	out, err = runCheck(t, "check-leaks", dir, "BASE="+commits[1])
	assert.NoError(t, err, "a leak before BASE failed the check:\n%s", out)
	// A new branch's first push: GitHub's before is forty zeros; only HEAD's own commit is scanned
	out, err = runCheck(t, "check-leaks", dir, "BASE="+strings.Repeat("0", 40))
	assert.NoError(t, err, "with BASE zeros the check scanned more than HEAD's commit:\n%s", out)
	out, err = runCheck(t, "check-leaks", dir, "BASE=")
	assert.NoError(t, err, out)
}
