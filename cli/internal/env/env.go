// Package env reads an environment's settings from infra/<env>/cluster/config.json, the one place they live.
package env

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Env is one environment of the homelab: production or the sandbox.
type Env struct {
	Name           string
	Root           string
	Domain         string
	SeedRepository string
	Kubeconfig     string
}

// Load reads infra/<name>/cluster/config.json in the repository at root.
func Load(root, name string) (Env, error) {
	if name != "production" && name != "sandbox" {
		return Env{}, fmt.Errorf("--env must be production or sandbox, not %q", name)
	}
	path := filepath.Join(root, "infra", name, "cluster", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return Env{}, err
	}
	var config struct {
		Domain         string `json:"domain"`
		SeedRepository string `json:"seed_repository"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return Env{}, fmt.Errorf("%s: %w", path, err)
	}
	if config.Domain == "" {
		return Env{}, fmt.Errorf("%s has no domain", path)
	}
	return Env{
		Name:           name,
		Root:           root,
		Domain:         config.Domain,
		SeedRepository: config.SeedRepository,
		Kubeconfig:     filepath.Join(root, "infra", name, "kubeconfig.yaml"),
	}, nil
}

// GitHubRepository is the seed repository as owner/name: the GitHub repository whose checks guard a deploy.
func (e Env) GitHubRepository() (string, error) {
	u, err := url.Parse(e.SeedRepository)
	if err != nil || u.Host != "github.com" {
		return "", fmt.Errorf("seed_repository %q is not a GitHub repository", e.SeedRepository)
	}
	path := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
	if strings.Count(path, "/") != 1 {
		return "", fmt.Errorf("seed_repository %q is not https://github.com/<owner>/<name>", e.SeedRepository)
	}
	return path, nil
}
