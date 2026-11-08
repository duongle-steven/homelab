package e2e

import (
	"context"
	"os"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/testenv"
)

// suiteDomain is the domain of the environment under test, read once; the offline tests (no ENV) get the sandbox's,
// which they never dial
var suiteDomain = func() string {
	env := os.Getenv("ENV")
	if _, ok := testenv.Dirs[env]; !ok {
		env = "dev"
	}
	domain, err := testenv.Domain(env)
	if err != nil {
		panic(err)
	}
	return domain
}()

// labEnv is the environment's name for lab's --env (sandbox or testbed)
var labEnv = func() string {
	if dir, ok := testenv.Dirs[os.Getenv("ENV")]; ok {
		return dir
	}
	return "sandbox"
}()

// testbedGatewayVIP is the address kube-vip gives testbed's Gateway (config.json's service_vips.gateway)
func testbedGatewayVIP() (string, error) {
	config, err := testenv.ReadEnvConfig("testbed")
	return config.ServiceVIPs.Gateway, err
}

// sandboxOnly skips a test outside the sandbox: it needs k3d (docker, the node's address, the localhost proxy) or the
// hostpath CSI driver
func sandboxOnly(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("ENV") != "dev" {
		t.Skip("runs against the sandbox only: " + why)
	}
}

// legacySkipUnlessSandbox gates the tests not on Ginkgo yet (14f-2 removes it)
func legacySkipUnlessSandbox(t *testing.T) {
	testenv.LocalOnly(t)
	waitForIdentityStack(context.TODO())
}
