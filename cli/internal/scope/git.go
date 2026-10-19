package scope

import (
	"fmt"
	"os/exec"
	"strings"
)

// Root is the repository's top directory.
func Root() (string, error) {
	out, err := git("", "rev-parse", "--show-toplevel")
	return strings.TrimSpace(out), err
}

// Changed lists the files the commits since base change, old and new paths of renames included. Uncommitted changes
// do not count: make dev tests the commit at HEAD.
func Changed(root, base string) ([]string, error) {
	if _, err := git(root, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return nil, fmt.Errorf("base %s does not exist: git fetch origin dev, or make dev scope=full", base)
	}
	out, err := git(root, "diff", "--name-only", "--no-renames", base+"...HEAD")
	return lines(out), err
}

// ChangedWorkingTree is Changed plus what is not committed yet (staged, unstaged and untracked files): the basic
// checks run before a commit.
func ChangedWorkingTree(root, base string) ([]string, error) {
	if _, err := git(root, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return nil, fmt.Errorf("base %s does not exist: git fetch origin dev, or pass --all", base)
	}
	mergeBase, err := git(root, "merge-base", base, "HEAD")
	if err != nil {
		return nil, err
	}
	diff, err := git(root, "diff", "--name-only", "--no-renames", strings.TrimSpace(mergeBase))
	if err != nil {
		return nil, err
	}
	untracked, err := git(root, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return append(lines(diff), lines(untracked)...), nil
}

// Tracked lists the repository's tracked files.
func Tracked(root string) ([]string, error) {
	out, err := git(root, "ls-files")
	return lines(out), err
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func lines(out string) []string {
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}
