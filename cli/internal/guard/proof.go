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

// proofValues is the part of system/argocd/values-<env>.yaml that says what the environment deploys
type proofValues struct {
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

// Gated is what an environment proves at a commit
type Gated struct {
	Paths  []string // globs of what it proves
	NotRun []string // globs taken out: a component switched off inside its chart
}

// GatedPaths is what proofEnv proves at commit: infra/<proofEnv>/release-paths.yaml's paths; every directory under
// system/, platform/ and apps/ that a directories entry of the root ApplicationSet in system/argocd/values-<proofEnv>.yaml
// matches and no excluded entry does; and the app files its apps ApplicationSet lists. The file's not_run takes out what
// is switched off inside a chart.
func GatedPaths(root, proofEnv, commit string) (Gated, error) {
	raw, err := git(root, "show", commit+":infra/"+proofEnv+"/release-paths.yaml")
	if err != nil {
		return Gated{}, err
	}
	var release struct {
		Paths  []string `yaml:"paths"`
		NotRun []string `yaml:"not_run"`
	}
	if err := yaml.Unmarshal([]byte(raw), &release); err != nil {
		return Gated{}, fmt.Errorf("infra/%s/release-paths.yaml at %.12s: %w", proofEnv, commit, err)
	}
	raw, err = git(root, "show", commit+":system/argocd/values-"+proofEnv+".yaml")
	if err != nil {
		return Gated{}, err
	}
	var values proofValues
	if err := yaml.Unmarshal([]byte(raw), &values); err != nil {
		return Gated{}, fmt.Errorf("system/argocd/values-%s.yaml at %.12s: %w", proofEnv, commit, err)
	}
	gated := Gated{Paths: append([]string{}, release.Paths...), NotRun: release.NotRun}
	var included, excluded []string
	for _, g := range values.ArgoCDApps.ApplicationSets["root"].Generators {
		for _, sub := range g.Matrix.Generators {
			for _, d := range sub.Git.Directories {
				if d.Exclude {
					excluded = append(excluded, d.Path)
				} else {
					included = append(included, d.Path)
				}
			}
		}
	}
	dirs, err := git(root, "ls-tree", "-d", "--name-only", commit, "system/", "platform/", "apps/")
	if err != nil {
		return Gated{}, err
	}
	for _, dir := range strings.Fields(dirs) {
		if matchesAny(included, dir) && !matchesAny(excluded, dir) {
			gated.Paths = append(gated.Paths, dir+"/**")
		}
	}
	for _, g := range values.ArgoCDApps.ApplicationSets["apps"].Generators {
		for _, sub := range g.Matrix.Generators {
			for _, f := range sub.Git.Files {
				if strings.HasPrefix(f.Path, "apps/") {
					gated.Paths = append(gated.Paths, f.Path)
				}
			}
		}
	}
	return gated, nil
}

// maxProofChecks bounds the gate's requests: GitHub allows 60 an hour without a token, make infra runs the gate twice
// and verify needs some of its own
const maxProofChecks = 20

// SkipContext is the commit status that records a --skip-proof: never proofEnv's own, or a later release would take the
// skip for a proof
func SkipContext(proofEnv string) string {
	return proofEnv + "/skipped"
}

// ProofGate passes when proofEnv proved what release changes: a candidate (newest first) with proofEnv's commit status
// whose gated paths did not change up to release, or no gated path changed since deployed (what production runs; empty:
// a first install, where everything counts as changed). It never passes when it cannot tell.
func ProofGate(ctx context.Context, root, repository, proofEnv, deployed, release string, candidates []string, gh GitHub) error {
	asked := 0
	capped := false
	for _, candidate := range candidates {
		gated, err := GatedPaths(root, proofEnv, candidate)
		if err != nil {
			// A commit from before the gate (no release-paths.yaml) proves nothing it can name
			continue
		}
		changed, err := changedFiles(root, candidate, release)
		if err != nil {
			return err
		}
		if len(gated.matching(changed)) != 0 {
			continue
		}
		if asked == maxProofChecks {
			capped = true
			break
		}
		asked++
		state, err := gh.CommitStatus(ctx, repository, candidate, proofEnv)
		if err != nil {
			return err
		}
		if state == "success" {
			return nil
		}
	}
	gated, err := GatedPaths(root, proofEnv, release)
	if err != nil {
		return err
	}
	changed, err := changedFiles(root, deployed, release)
	if err != nil {
		return err
	}
	hits := gated.matching(changed)
	if len(hits) == 0 {
		return nil
	}
	if len(hits) > 10 {
		hits = append(hits[:10], fmt.Sprintf("and %d more", len(hits)-10))
	}
	found := "has not proved"
	if capped {
		found = fmt.Sprintf("has no mark on the %d commits asked about, an older one goes unseen, and has not proved", maxProofChecks)
	}
	return fmt.Errorf("%s %s what %.12s changes (%s): run make %s on dev's head, or --skip-proof=<reason> "+
		"(docs/installation/testbed.md)", proofEnv, found, release, strings.Join(hits, ", "), proofEnv)
}

// SkipProofReason refuses a --skip-proof without a reason someone can read later
func SkipProofReason(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("--skip-proof needs a reason")
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
		out, err = git(root, "diff", "--name-only", "--no-renames", from, to)
	}
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

func (g Gated) matching(files []string) []string {
	var hits []string
	for _, f := range files {
		if matchesAny(g.Paths, f) && !matchesAny(g.NotRun, f) {
			hits = append(hits, f)
		}
	}
	sort.Strings(hits)
	return hits
}

func matchesAny(globs []string, path string) bool {
	for _, g := range globs {
		if ok, _ := doublestar.Match(g, path); ok {
			return true
		}
	}
	return false
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

// ProofCandidates are the commits a mark may sit on for a release whose pull request head is head: head, then its history
// back to deployed (exclusive; the newest 200 when nothing is deployed), merged branches included. make testbed marks a
// branch's tip or dev's head, so merges and their parents come first: the gate asks about only maxProofChecks of them.
func ProofCandidates(root, deployed, head string) ([]string, error) {
	args := []string{"rev-list", "--parents", head}
	if deployed != "" {
		args = append(args, "^"+deployed)
	} else {
		args = append(args, "--max-count=200")
	}
	out, err := git(root, args...)
	if err != nil {
		return nil, err
	}
	likely := map[string]bool{head: true}
	var commits []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		commits = append(commits, fields[0])
		if len(fields) > 2 {
			for _, c := range fields {
				likely[c] = true
			}
		}
	}
	sort.SliceStable(commits, func(i, j int) bool { return likely[commits[i]] && !likely[commits[j]] })
	return commits, nil
}
