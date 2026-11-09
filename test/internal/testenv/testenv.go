// Package testenv is the environment under test: ENV, its infra/<env> directory, its domain and the sandbox's node
package testenv

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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

// ReadEnvConfig reads infra/<dir>/cluster/config.json for an ENV value
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

// K3dNode is the sandbox's k3s node (infra/_modules/k3d/k3d.yaml). ServiceLB publishes the Gateway on its IP; the tests
// connect there directly instead of through the localhost socat proxy, whose connects to :443 occasionally
// stall for ~4.5 s and hang the browser.
const K3dNode = "k3d-homelab-dev-server-0"

// NodeAddressFromInspect parses docker inspect's IP address output.
func NodeAddressFromInspect(out string) (string, error) {
	addr := strings.TrimSpace(out)
	if net.ParseIP(addr) == nil {
		return "", fmt.Errorf("no IP address for %s in %q", K3dNode, out)
	}
	return addr, nil
}
