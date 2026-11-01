package guard

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v3"
)

// StagingContext is the commit status make staging writes on the commit whose whole suite passed on staging
// (scripts/staging-mark)
const StagingContext = "staging"

// stagingValues is the part of system/argocd/values-staging.yaml that says what staging deploys
type stagingValues struct {
	ArgoCDApps struct {
		ApplicationSets map[string]struct {
			Generators []struct {
				Matrix struct {
					Generators []struct {
						Git struct {
							Directories []struct {
								Path    string `yaml:"path"`
								Exclude bool   `yaml:"exclude"`
							} `yaml:"directories"`
							Files []struct {
								Path string `yaml:"path"`
							} `yaml:"files"`
						} `yaml:"git"`
					} `yaml:"generators"`
				} `yaml:"matrix"`
			} `yaml:"generators"`
		} `yaml:"applicationsets"`
	} `yaml:"argocd-apps"`
}

// GatedPaths are the globs of what staging proves at commit: infra/staging/release-paths.yaml's, every component
// under system/, platform/ and apps/ the root ApplicationSet of system/argocd/values-staging.yaml does not exclude,
// and the app files its apps ApplicationSet lists
func GatedPaths(root, commit string) ([]string, error) {
	raw, err := git(root, "show", commit+":infra/staging/release-paths.yaml")
	if err != nil {
		return nil, err
	}
	var release struct {
		Paths []string `yaml:"paths"`
	}
	if err := yaml.Unmarshal([]byte(raw), &release); err != nil {
		return nil, fmt.Errorf("infra/staging/release-paths.yaml at %.12s: %w", commit, err)
	}
	raw, err = git(root, "show", commit+":system/argocd/values-staging.yaml")
	if err != nil {
		return nil, err
	}
	var values stagingValues
	if err := yaml.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("system/argocd/values-staging.yaml at %.12s: %w", commit, err)
	}
	gated := append([]string{}, release.Paths...)
	excluded := map[string]bool{}
	for _, g := range values.ArgoCDApps.ApplicationSets["root"].Generators {
		for _, sub := range g.Matrix.Generators {
			for _, d := range sub.Git.Directories {
				if d.Exclude {
					excluded[d.Path] = true
				}
			}
		}
	}
	dirs, err := git(root, "ls-tree", "-d", "--name-only", commit, "system/", "platform/", "apps/")
	if err != nil {
		return nil, err
	}
	for _, dir := range strings.Fields(dirs) {
		if !excluded[dir] {
			gated = append(gated, dir+"/**")
		}
	}
	for _, g := range values.ArgoCDApps.ApplicationSets["apps"].Generators {
		for _, sub := range g.Matrix.Generators {
			for _, f := range sub.Git.Files {
				if strings.HasPrefix(f.Path, "apps/") {
					gated = append(gated, f.Path)
				}
			}
		}
	}
	return gated, nil
}

// StagingGate passes when staging proved what release changes: a candidate (newest first) marked by make staging
// whose gated paths did not change up to release, or no gated path changed since deployed (what production runs;
// empty: a first install, where everything counts as changed). It never passes when it cannot tell.
func StagingGate(ctx context.Context, root, repository, deployed, release string, candidates []string, gh GitHub) error {
	for _, candidate := range candidates {
		state, err := gh.CommitStatus(ctx, repository, candidate, StagingContext)
		if err != nil {
			return err
		}
		if state != "success" {
			continue
		}
		gated, err := GatedPaths(root, candidate)
		if err != nil {
			// A commit from before the gate (no release-paths.yaml) proves nothing it can name
			continue
		}
		changed, err := changedFiles(root, candidate, release)
		if err != nil {
			return err
		}
		if len(matching(gated, changed)) == 0 {
			return nil
		}
	}
	gated, err := GatedPaths(root, release)
	if err != nil {
		return err
	}
	changed, err := changedFiles(root, deployed, release)
	if err != nil {
		return err
	}
	hits := matching(gated, changed)
	if len(hits) == 0 {
		return nil
	}
	if len(hits) > 10 {
		hits = append(hits[:10], fmt.Sprintf("and %d more", len(hits)-10))
	}
	return fmt.Errorf("staging has not proved what %.12s changes (%s): run make staging on dev's head, or "+
		"--skip-staging=<reason> (docs/installation/staging.md)", release, strings.Join(hits, ", "))
}

// SkipStagingReason refuses a --skip-staging without a reason someone can read later
func SkipStagingReason(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("--skip-staging needs a reason")
	}
	return nil
}

// changedFiles are the files that differ between from and to; every file of to when from is empty
func changedFiles(root, from, to string) ([]string, error) {
	var out string
	var err error
	if from == "" {
		out, err = git(root, "ls-tree", "-r", "--name-only", to)
	} else {
		out, err = git(root, "diff", "--name-only", from, to)
	}
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

func matching(globs, files []string) []string {
	var hits []string
	for _, f := range files {
		for _, g := range globs {
			if ok, _ := doublestar.Match(g, f); ok {
				hits = append(hits, f)
				break
			}
		}
	}
	sort.Strings(hits)
	return hits
}

// CommitStatus is the state of the commit status named context on sha ("" when none was written)
func (g GitHub) CommitStatus(ctx context.Context, repository, sha, context string) (string, error) {
	var combined struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if err := g.get(ctx, "/repos/"+repository+"/commits/"+sha+"/status", url.Values{"per_page": {"100"}}, &combined); err != nil {
		return "", err
	}
	for _, s := range combined.Statuses {
		if s.Context == context {
			return s.State, nil
		}
	}
	return "", nil
}

// StagingCandidates are the commits a staging mark may sit on for a release whose pull request head is head: head,
// then its first-parent history back to deployed (exclusive; the newest 200 when nothing is deployed)
func StagingCandidates(root, deployed, head string) ([]string, error) {
	args := []string{"rev-list", "--first-parent", "--max-count=200", head}
	if deployed != "" {
		args = append(args, "^"+deployed)
	}
	out, err := git(root, args...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}
