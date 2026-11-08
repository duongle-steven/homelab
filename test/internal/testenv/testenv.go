package testenv

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/repo"
	"github.com/stretchr/testify/require"
)

// Dirs maps the suite's ENV to the environment's directory in infra/; production is never a test target
var Dirs = map[string]string{"dev": "sandbox", "testbed": "testbed"}

// Config is the part of infra/<env>/cluster/config.json the suite reads
type Config struct {
	Domain      string `json:"domain"`
	ServiceVIPs struct {
		Gateway string `json:"gateway"`
	} `json:"service_vips"`
}

func ReadEnvConfig(env string) (Config, error) {
	var config Config
	dir, ok := Dirs[env]
	if !ok {
		return config, fmt.Errorf("ENV=%q: the suite runs against the sandbox (dev) or testbed only", env)
	}
	raw, err := os.ReadFile(filepath.Join(repo.Root(), "infra", dir, "cluster", "config.json"))
	if err != nil {
		return config, err
	}
	return config, json.Unmarshal(raw, &config)
}

// Domain is the domain of the environment ENV names (infra/<env>/cluster/config.json)
func Domain(env string) (string, error) {
	config, err := ReadEnvConfig(env)
	return config.Domain, err
}

// SuiteDomain is the domain of the environment under test: a host is "<app>." + SuiteDomain(t)
func SuiteDomain(t *testing.T) string {
	t.Helper()
	domain, err := Domain(os.Getenv("ENV"))
	require.NoError(t, err)
	return domain
}

// LocalOnly skips a test outside the sandbox and testbed: it needs the local CA, the tests' fixtures or OpenBao's
// admin role, which production does not give the suite
func LocalOnly(t *testing.T) {
	t.Helper()
	if _, ok := Dirs[os.Getenv("ENV")]; !ok {
		t.Skip("runs against the sandbox or testbed only")
	}
}

func LogsSandboxOnly(t *testing.T) { SecretsSandboxOnly(t) }

func SecretsSandboxOnly(t *testing.T) { LocalOnly(t) }
