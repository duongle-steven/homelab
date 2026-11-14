package offline

import (
	"testing"

	"github.com/duongle-steven/homelab/test/internal/render"
	"github.com/stretchr/testify/assert"
)

// Only the Sensor starts a run that publishes (event=push), every run starts at the golden path's entrypoint, and a
// run's arguments do not change afterwards: Strict template referencing still lets a submitter set both, so an editor
// could otherwise sign what the default branch never built (step 10f's final review). The CI e2e spec CIRestrictions
// proves the refusals through the API server; this pins the policy's shape
func TestCIWorkflowPolicy(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		policy := `select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == "ci-workflow-arguments")`
		assert.Equal(t, "Fail", render.Yq(t, "platform/ci", env, policy+` | .spec.failurePolicy`), env)
		assert.Equal(t, "argoproj.io workflows CREATE,UPDATE", render.Yq(t, "platform/ci", env,
			policy+` | .spec.matchConstraints.resourceRules[] | .apiGroups[0] + " " + .resources[0] + " " + (.operations | join(","))`), env)
		assert.Contains(t, render.Yq(t, "platform/ci", env, policy+` | .spec.validations[1].expression`), "'system:serviceaccount:ci:ci-sensor'", env)
		binding := `select(.kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == "ci-workflow-arguments")`
		assert.Equal(t, "Deny ci", render.Yq(t, "platform/ci", env,
			binding+` | (.spec.validationActions | join(",")) + " " + .spec.matchResources.namespaceSelector.matchLabels["kubernetes.io/metadata.name"]`), env)
	}
}
