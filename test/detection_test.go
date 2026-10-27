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
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
		}
		for name := range set {
			if strings.HasPrefix(name, "scripts/") {
				require.NoError(t, os.Chmod(filepath.Join(dir, name), 0o755))
			}
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
	assert.Contains(t, out, "config.env", "the finding names its file")
	out, err = runCheck(t, "check-leaks", dir, "BASE="+commits[1])
	assert.NoError(t, err, "a leak before BASE failed the check:\n%s", out)
	// A new branch's first push: GitHub's before is forty zeros; only HEAD's own commit is scanned
	out, err = runCheck(t, "check-leaks", dir, "BASE="+strings.Repeat("0", 40))
	assert.NoError(t, err, "with BASE zeros the check scanned more than HEAD's commit:\n%s", out)
	out, err = runCheck(t, "check-leaks", dir, "BASE=")
	assert.NoError(t, err, out)
}

// fakeTrivy writes a trivy that records its arguments in dir/args (one call per line) and exits with code
func fakeTrivy(t *testing.T, code string) (string, string) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"$*\" >> " + dir + "/args\nexit " + code + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "trivy"), []byte(script), 0o755))
	return filepath.Join(dir, "trivy"), filepath.Join(dir, "args")
}

// iac runs Trivy's config checks once over the repository, Dockerfiles and OpenTofu only (Kubernetes is Kyverno's),
// failing on HIGH and CRITICAL; .trivyignore.yaml's paths are from the root, so one entry cannot hide another file's
func TestCheckIaC(t *testing.T) {
	trivy, args := fakeTrivy(t, "0")
	out, err := runCheck(t, "check-iac", "..", "TRIVY="+trivy)
	require.NoError(t, err, out)
	calls, err := os.ReadFile(args)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	require.Len(t, lines, 1, "one scan from the root")
	for _, want := range []string{"config ", "--misconfig-scanners dockerfile,terraform", "--severity HIGH,CRITICAL", "--exit-code 1",
		"--ignorefile .trivyignore.yaml", "--skip-dirs **/.terragrunt-cache", "--skip-dirs .state"} {
		assert.Contains(t, lines[0], want)
	}
	assert.True(t, strings.HasSuffix(lines[0], " ."), lines[0])
	failing, _ := fakeTrivy(t, "1")
	_, err = runCheck(t, "check-iac", "..", "TRIVY="+failing)
	assert.Error(t, err, "a finding passed")
}

// fakeRenderer is a scripts/render-chart that prints the target's file, and fails for a target named broken
const fakeRenderer = "#!/bin/sh\n[ \"$1\" = charts/broken.yaml ] && { echo 'render failed' >&2; exit 1; }\ncat \"$1\"\n"

// images scans only the images a change brings: new at HEAD in the charts it touches, digest and registry port kept
func TestCheckImages(t *testing.T) {
	podWith := func(images ...string) string {
		var b strings.Builder
		b.WriteString("kind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n")
		for _, i := range images {
			b.WriteString("        - image: " + i + "\n")
		}
		return b.String()
	}
	pinned := "registry.example:5000/team/app@sha256:" + strings.Repeat("a", 64)
	dir, commits := gitRepo(t,
		map[string]string{"scripts/render-chart": fakeRenderer, "charts/web.yaml": podWith("docker.io/library/nginx:1.29.0", "quay.io/x/kept:1.0")},
		map[string]string{"charts/web.yaml": podWith("docker.io/library/nginx:1.29.1", "quay.io/x/kept:1.0", pinned) +
			"---\nkind: Cluster\nspec:\n  imageName: ghcr.io/cloudnative-pg/postgresql:18.1\n" +
			"---\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n        - image: quay.io/x/kept:1.0\n" +
			"          env:\n            - name: ROOK_CSIADDONS_IMAGE\n              value: quay.io/csiaddons/k8s-sidecar:v0.14.0\n" +
			"            - name: LOG_LEVEL\n              value: info\n",
			"charts/new.yaml": podWith("ghcr.io/x/new:2.0"), "charts/broken.yaml": ""})
	base := "BASE=" + commits[0]

	out, err := runCheck(t, "check-images", dir, base, "TARGETS=charts/web.yaml charts/new.yaml", "IMAGES_ONLY=1")
	require.NoError(t, err, out)
	assert.Equal(t, []string{"docker.io/library/nginx:1.29.1", "ghcr.io/cloudnative-pg/postgresql:18.1", "ghcr.io/x/new:2.0",
		"quay.io/csiaddons/k8s-sidecar:v0.14.0", pinned},
		strings.Fields(out), "only the images new at HEAD, whole")

	_, err = runCheck(t, "check-images", dir, base, "TARGETS=charts/web.yaml charts/broken.yaml", "IMAGES_ONLY=1")
	assert.Error(t, err, "a render that fails must fail the check")

	trivy, args := fakeTrivy(t, "1")
	_, err = runCheck(t, "check-images", dir, base, "TARGETS=charts/new.yaml", "TRIVY="+trivy)
	assert.Error(t, err, "a finding passed")
	calls, _ := os.ReadFile(args)
	assert.Regexp(t, `image .*--severity CRITICAL .*--ignore-unfixed .*--exit-code 1 .*ghcr.io/x/new:2.0`, string(calls))

	out, err = runCheck(t, "check-images", dir, "BASE="+commits[1], "TARGETS=", "IMAGES_ONLY=1")
	assert.NoError(t, err, out)
	assert.Empty(t, strings.Fields(out), "no target, nothing to scan")
}
