package forgejo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// local is an Endpoint whose repository is a bare repository on disk (file://), and a checkout with one commit.
func local(t *testing.T) (Endpoint, string, string) {
	t.Helper()
	server := t.TempDir()
	run(t, t.TempDir(), "init", "-q", "--bare", "-b", "master", filepath.Join(server, "ops", "homelab.git"))
	checkout := t.TempDir()
	run(t, checkout, "init", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(checkout, "file"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, checkout, "add", "file")
	run(t, checkout, "commit", "-q", "-m", "one")
	return Endpoint{URL: "file://" + server}, checkout, run(t, checkout, "rev-parse", "HEAD")
}

func TestPush(t *testing.T) {
	ep, checkout, head := local(t)
	ctx := context.Background()
	if got, err := Deployed(ctx, ep, checkout, "secret-token"); err != nil || got != "" {
		t.Fatalf("Deployed() on an empty repository = %q, %v", got, err)
	}
	if err := Push(ctx, ep, checkout, "secret-token", head); err != nil {
		t.Fatal(err)
	}
	if got, err := Deployed(ctx, ep, checkout, "secret-token"); err != nil || got != head {
		t.Fatalf("Deployed() = %q, %v; want %s", got, err, head)
	}
	if err := Push(ctx, ep, checkout, "secret-token", head); err != nil {
		t.Fatalf("pushing the deployed commit again: %v", err)
	}
}

func TestPushNeverForces(t *testing.T) {
	ep, checkout, head := local(t)
	ctx := context.Background()
	if err := Push(ctx, ep, checkout, "t", head); err != nil {
		t.Fatal(err)
	}
	run(t, checkout, "commit", "-q", "--amend", "-m", "rewritten")
	if err := Push(ctx, ep, checkout, "t", run(t, checkout, "rev-parse", "HEAD")); err == nil {
		t.Fatal("a non-fast-forward push succeeded")
	}
}

// Review Focus 4: the sandbox's ops/homelab holds a commit that is not an ancestor of HEAD (a rebased branch)
func TestSandboxCommitFastForwards(t *testing.T) {
	ep, checkout, _ := local(t)
	ctx := context.Background()
	first, err := SandboxCommit(ctx, ep, checkout, "t")
	if err != nil {
		t.Fatal(err)
	}
	if err := Push(ctx, ep, checkout, "t", first); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "file"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, checkout, "commit", "-q", "--amend", "-am", "rebased")
	second, err := SandboxCommit(ctx, ep, checkout, "t")
	if err != nil {
		t.Fatal(err)
	}
	if err := Push(ctx, ep, checkout, "t", second); err != nil {
		t.Fatalf("the sandbox push after a rebase: %v", err)
	}
	if got, want := run(t, checkout, "rev-parse", second+"^{tree}"), run(t, checkout, "rev-parse", "HEAD^{tree}"); got != want {
		t.Fatalf("the sandbox commit's tree %s is not HEAD's %s", got, want)
	}
	if got := run(t, checkout, "rev-parse", second+"^"); got != first {
		t.Fatalf("the sandbox commit's parent %s is not the deployed %s", got, first)
	}
}

func TestGitKeepsTheTokenOutOfArguments(t *testing.T) {
	ep := Endpoint{URL: "https://git.example.dev", Git: []string{"http.sslCAInfo=/ca.crt"}}
	cmd, cleanup, err := ep.git(context.Background(), t.TempDir(), "secret-token", "push", ep.RepositoryURL(), "HEAD:refs/heads/master")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if strings.Contains(strings.Join(cmd.Args, " "), "secret-token") {
		t.Fatalf("the token is in git's arguments: %v", cmd.Args)
	}
	var askpass string
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "GIT_ASKPASS=") {
			askpass = strings.TrimPrefix(e, "GIT_ASKPASS=")
		}
	}
	for prompt, want := range map[string]string{"Username for 'https://git.example.dev': ": "deploy", "Password for 'https://deploy@git.example.dev': ": "secret-token"} {
		// The askpass reads the token from the environment git passes it
		c := exec.Command(askpass, prompt)
		c.Env = cmd.Env
		out, err := c.Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Errorf("askpass %q = %q, %v; want %q", prompt, out, err, want)
		}
	}
}

// The checkout's hooks run in the user's repository: they must not run with deploy's token in their environment
func TestPushRunsNoHooks(t *testing.T) {
	ep, checkout, head := local(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(checkout, ".git", "hooks", "pre-push")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nenv > "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Push(context.Background(), ep, checkout, "secret-token", head); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the pre-push hook ran during the deploy push")
	}
}

func TestFastForwards(t *testing.T) {
	ep, checkout, head := local(t)
	ctx := context.Background()
	if err := FastForwards(ctx, ep, checkout, "t", head); err != nil {
		t.Fatalf("an empty repository: %v", err)
	}
	if err := Push(ctx, ep, checkout, "t", head); err != nil {
		t.Fatal(err)
	}
	run(t, checkout, "commit", "-q", "--allow-empty", "-m", "two")
	if err := FastForwards(ctx, ep, checkout, "t", run(t, checkout, "rev-parse", "HEAD")); err != nil {
		t.Fatalf("a descendant: %v", err)
	}
	run(t, checkout, "checkout", "-q", "--orphan", "other")
	run(t, checkout, "commit", "-q", "--allow-empty", "-m", "unrelated")
	if err := FastForwards(ctx, ep, checkout, "t", run(t, checkout, "rev-parse", "HEAD")); err == nil || !strings.Contains(err.Error(), "does not descend") {
		t.Fatalf("an unrelated commit: %v", err)
	}
}
