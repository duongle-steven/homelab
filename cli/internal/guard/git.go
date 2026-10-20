package guard

import (
	"fmt"
	"os/exec"
	"strings"
)

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// releaseCommit is HEAD when the checkout is origin/master without uncommitted changes to tracked files.
func releaseCommit(root string) (string, error) {
	status, err := git(root, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", fmt.Errorf("uncommitted changes: production deploys only origin/master as it is\n%s", status)
	}
	if _, err := git(root, "fetch", "--quiet", "origin", "master"); err != nil {
		return "", err
	}
	head, err := git(root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	master, err := git(root, "rev-parse", "origin/master")
	if err != nil {
		return "", err
	}
	if head != master {
		return "", fmt.Errorf("HEAD %.12s is not origin/master %.12s: production deploys only origin/master (git switch master && git pull)", head, master)
	}
	return head, nil
}
