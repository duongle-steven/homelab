package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The suite's domain is the environment's (infra/<env>/cluster/config.json): the sandbox's under ENV=dev, staging's
// under ENV=staging
func TestTestDomain(t *testing.T) {
	t.Setenv("ENV", "dev")
	assert.Equal(t, "127-0-0-1.nip.io", testDomain(t))
	t.Setenv("ENV", "staging")
	assert.Equal(t, "192-168-100-2.nip.io", testDomain(t))
}

// Production is never a test target: its domain is refused rather than pointing the suite at it
func TestEnvDomainRefusesProduction(t *testing.T) {
	_, err := envDomain("prod")
	assert.Error(t, err)
}
