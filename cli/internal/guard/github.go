package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// GitHub reads check runs and pull requests from GitHub's REST API without a token (the repository is public).
type GitHub struct {
	BaseURL string // https://api.github.com
	HTTP    *http.Client
}

// Run is one check run.
type Run struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"html_url"`
}

func (g GitHub) get(ctx context.Context, path string, query url.Values, into any) error {
	u := g.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GitHub API %s: %s: %s", path, resp.Status, body)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// LatestRun is the newest check run named name on commit sha (the highest ID: a rerun or a second workflow
// reporting the same name lists several), or nil when none ran.
func (g GitHub) LatestRun(ctx context.Context, repository, sha, name string) (*Run, error) {
	var page struct {
		Runs []Run `json:"check_runs"`
	}
	if err := g.get(ctx, "/repos/"+repository+"/commits/"+sha+"/check-runs", url.Values{"check_name": {name}, "per_page": {"100"}}, &page); err != nil {
		return nil, err
	}
	var latest *Run
	for i := range page.Runs {
		if latest == nil || page.Runs[i].ID > latest.ID {
			latest = &page.Runs[i]
		}
	}
	return latest, nil
}

// MergedPullHead is the head commit of the pull request merged into master as sha, or "" when none was.
func (g GitHub) MergedPullHead(ctx context.Context, repository, sha string) (string, error) {
	var pulls []struct {
		MergedAt       *string `json:"merged_at"`
		MergeCommitSHA string  `json:"merge_commit_sha"`
		Base           struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := g.get(ctx, "/repos/"+repository+"/commits/"+sha+"/pulls", nil, &pulls); err != nil {
		return "", err
	}
	for _, p := range pulls {
		if p.MergedAt != nil && *p.MergedAt != "" && p.Base.Ref == "master" && p.MergeCommitSHA == sha {
			return p.Head.SHA, nil
		}
	}
	return "", nil
}
