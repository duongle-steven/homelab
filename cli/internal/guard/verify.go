// Package guard decides whether a commit may reach production: origin/master, green checks, a merged pull request.
package guard

import (
	"context"
	"fmt"
)

// Verify returns HEAD when it may be deployed to production, or why not. It never passes when it cannot tell.
func Verify(ctx context.Context, root, repository string, gh GitHub) (string, error) {
	head, err := releaseCommit(root)
	if err != nil {
		return "", err
	}
	if err := succeeded(ctx, gh, repository, head, "checks"); err != nil {
		return "", err
	}
	pullHead, err := gh.MergedPullHead(ctx, repository, head)
	if err != nil {
		return "", err
	}
	if pullHead == "" {
		return "", fmt.Errorf("no pull request into master was merged as %.12s: production deploys only merged pull requests", head)
	}
	if err := succeeded(ctx, gh, repository, pullHead, "sandbox"); err != nil {
		return "", err
	}
	return head, nil
}

func succeeded(ctx context.Context, gh GitHub, repository, sha, name string) error {
	run, err := gh.LatestRun(ctx, repository, sha, name)
	if err != nil {
		return err
	}
	switch {
	case run == nil:
		return fmt.Errorf("check %s has not run on %.12s", name, sha)
	case run.Status != "completed":
		return fmt.Errorf("check %s on %.12s is %s: wait for it and run again (%s)", name, sha, run.Status, run.URL)
	case run.Conclusion != "success":
		return fmt.Errorf("check %s on %.12s concluded %s (%s)", name, sha, run.Conclusion, run.URL)
	}
	return nil
}
