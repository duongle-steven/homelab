package test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// envDirs maps the suite's ENV to the environment's directory in infra/; production is never a test target
var envDirs = map[string]string{"dev": "sandbox", "testbed": "testbed"}

// envConfig is the part of infra/<env>/cluster/config.json the suite reads
type envConfig struct {
	Domain      string `json:"domain"`
	ServiceVIPs struct {
		Gateway string `json:"gateway"`
	} `json:"service_vips"`
}

func readEnvConfig(env string) (envConfig, error) {
	var config envConfig
	dir, ok := envDirs[env]
	if !ok {
		return config, fmt.Errorf("ENV=%q: the suite runs against the sandbox (dev) or testbed only", env)
	}
	raw, err := os.ReadFile(filepath.Join("..", "infra", dir, "cluster", "config.json"))
	if err != nil {
		return config, err
	}
	return config, json.Unmarshal(raw, &config)
}

// envDomain is the domain of the environment ENV names (infra/<env>/cluster/config.json)
func envDomain(env string) (string, error) {
	config, err := readEnvConfig(env)
	return config.Domain, err
}

// suiteDomain is the domain of the environment under test, read once; the offline tests (no ENV) get the sandbox's,
// which they never dial
var suiteDomain = func() string {
	env := os.Getenv("ENV")
	if _, ok := envDirs[env]; !ok {
		env = "dev"
	}
	domain, err := envDomain(env)
	if err != nil {
		panic(err)
	}
	return domain
}()

// labEnv is the environment's name for lab's --env (sandbox or testbed)
var labEnv = func() string {
	if dir, ok := envDirs[os.Getenv("ENV")]; ok {
		return dir
	}
	return "sandbox"
}()

// testbedGatewayVIP is the address kube-vip gives testbed's Gateway (config.json's service_vips.gateway)
func testbedGatewayVIP() (string, error) {
	config, err := readEnvConfig("testbed")
	return config.ServiceVIPs.Gateway, err
}

// testDomain is the domain of the environment under test: a host is "<app>." + testDomain(t)
func testDomain(t *testing.T) string {
	t.Helper()
	domain, err := envDomain(os.Getenv("ENV"))
	require.NoError(t, err)
	return domain
}

// localOnly skips a test outside the sandbox and testbed: it needs the local CA, the tests' fixtures or OpenBao's
// admin role, which production does not give the suite
func localOnly(t *testing.T) {
	t.Helper()
	if _, ok := envDirs[os.Getenv("ENV")]; !ok {
		t.Skip("runs against the sandbox or testbed only")
	}
}

// sandboxOnly skips a test outside the sandbox: it needs k3d (docker, the node's address, the localhost proxy) or the
// hostpath CSI driver
func sandboxOnly(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("ENV") != "dev" {
		t.Skip("runs against the sandbox only: " + why)
	}
}
