package forgejo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// git runs git in dir as deploy with token, through an askpass script that reads the token from the environment:
// it reaches neither git's arguments nor its output.
func (ep Endpoint) git(ctx context.Context, dir, token string, args ...string) (*exec.Cmd, func(), error) {
	temp, err := os.MkdirTemp("", "lab-git-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(temp) }
	askpass := filepath.Join(temp, "askpass")
	script := "#!/bin/sh\ncase \"$1\" in\n  Username*) echo deploy ;;\n  *) printf '%s\\n' \"$LAB_FORGEJO_TOKEN\" ;;\nesac\n"
	if err := os.WriteFile(askpass, []byte(script), 0o700); err != nil {
		cleanup()
		return nil, nil, err
	}
	// No hooks: they are the checkout's code and would run with the token in their environment
	full := []string{"-C", dir, "-c", "credential.helper=", "-c", "core.hooksPath=/dev/null"}
	for _, c := range ep.Git {
		full = append(full, "-c", c)
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="+askpass, "LAB_FORGEJO_TOKEN="+token,
		"GIT_AUTHOR_NAME=lab", "GIT_AUTHOR_EMAIL=lab@noreply.invalid", "GIT_COMMITTER_NAME=lab", "GIT_COMMITTER_EMAIL=lab@noreply.invalid")
	return cmd, cleanup, nil
}

func (ep Endpoint) output(ctx context.Context, dir, token string, args ...string) (string, error) {
	cmd, cleanup, err := ep.git(ctx, dir, token, args...)
	if err != nil {
		return "", err
	}
	defer cleanup()
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(exit.Stderr))
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

// Deployed is ops/homelab's master on Forgejo, or "" while the repository is empty.
func Deployed(ctx context.Context, ep Endpoint, root, token string) (string, error) {
	out, err := ep.output(ctx, root, token, "ls-remote", ep.RepositoryURL(), "refs/heads/"+Branch)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", nil
	}
	return strings.Fields(out)[0], nil
}

// Push publishes commit as ops/homelab's master, never forced; it does nothing when master already is commit.
func Push(ctx context.Context, ep Endpoint, root, token, commit string) error {
	deployed, err := Deployed(ctx, ep, root, token)
	if err != nil {
		return err
	}
	if deployed == commit {
		return nil
	}
	_, err = ep.output(ctx, root, token, "push", "--quiet", ep.RepositoryURL(), commit+":refs/heads/"+Branch)
	return err
}

// SandboxCommit is a commit with HEAD's tree on top of the sandbox's deployed master (none while it is empty), so
// the sandbox push is a fast-forward even after a rebase. The sandbox is not production: it only needs the content.
func SandboxCommit(ctx context.Context, ep Endpoint, root, token string) (string, error) {
	head, err := ep.output(ctx, root, token, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	deployed, err := Deployed(ctx, ep, root, token)
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", "HEAD^{tree}", "-m", "sandbox: " + head}
	if deployed != "" {
		if _, err := ep.output(ctx, root, token, "fetch", "--quiet", ep.RepositoryURL(), Branch); err != nil {
			return "", err
		}
		tree, err := ep.output(ctx, root, token, "rev-parse", deployed+"^{tree}")
		if err != nil {
			return "", err
		}
		headTree, err := ep.output(ctx, root, token, "rev-parse", "HEAD^{tree}")
		if err != nil {
			return "", err
		}
		if tree == headTree {
			return deployed, nil
		}
		args = append(args, "-p", deployed)
	}
	return ep.output(ctx, root, token, args...)
}

// FastForwards refuses a commit that does not descend from ops/homelab's master (an empty repository accepts any):
// a break-glass deploy checks it before applying anything, since the push that would refuse it comes last.
func FastForwards(ctx context.Context, ep Endpoint, root, token, commit string) error {
	deployed, err := Deployed(ctx, ep, root, token)
	if err != nil || deployed == "" {
		return err
	}
	if _, err := ep.output(ctx, root, token, "fetch", "--quiet", ep.RepositoryURL(), Branch); err != nil {
		return err
	}
	if _, err := ep.output(ctx, root, token, "merge-base", "--is-ancestor", deployed, commit); err != nil {
		return fmt.Errorf("%.12s does not descend from ops/homelab's master %.12s: deploy a commit on top of it", commit, deployed)
	}
	return nil
}
