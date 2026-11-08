package offline

import (
	"testing"

	"github.com/duongle-steven/homelab/test/internal/testenv"
	"github.com/stretchr/testify/assert"
)

// The suite's domain is the environment's (infra/<env>/cluster/config.json): the sandbox's under ENV=dev, testbed's
// under ENV=testbed
func TestTestDomain(t *testing.T) {
	t.Setenv("ENV", "dev")
	assert.Equal(t, "127-0-0-1.nip.io", testenv.SuiteDomain(t))
	t.Setenv("ENV", "testbed")
	assert.Equal(t, "192-168-100-2.nip.io", testenv.SuiteDomain(t))
}

// Production is never a test target: its domain is refused rather than pointing the suite at it
func TestEnvDomainRefusesProduction(t *testing.T) {
	_, err := testenv.Domain("prod")
	assert.Error(t, err)
}
