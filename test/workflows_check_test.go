package test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// workflows fails on a weakness zizmor knows (a pull_request_target checking out the head) and passes .github/
func TestCheckWorkflows(t *testing.T) {
	out, err := runCheck(t, "check-workflows", "..")
	assert.NoError(t, err, out)
	out, err = runCheck(t, "check-workflows", "..", "WORKFLOWS_DIR=test/fixtures/workflows/bad")
	assert.Error(t, err, out)
	assert.Contains(t, out, "dangerous-triggers")
}

// Every finding ignored in .github/zizmor.yml says why, on its line
func TestZizmorIgnoresHaveReasons(t *testing.T) {
	data, err := os.ReadFile("../.github/zizmor.yml")
	if os.IsNotExist(err) {
		return
	}
	assert.NoError(t, err)
	inIgnore := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ignore:") {
			inIgnore = true
			continue
		}
		if inIgnore && strings.HasPrefix(trimmed, "- ") {
			assert.Contains(t, trimmed, "#", "an ignored finding without a reason: %s", trimmed)
		} else if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			inIgnore = false
		}
	}
}
