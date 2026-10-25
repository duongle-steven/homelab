package test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The scheduled rotation (platform/secrets-rotation): daily at 03:00, one run at a time, suspended until production
// has lab's image; it may exec only into ntfy and delete claims only in woodpecker (step 11b)
func TestSecretsCronJobRender(t *testing.T) {
	const cron = `select(.kind == "CronJob")`
	assert.Equal(t, `0 3 * * *`, renderVelero(t, "platform/secrets-rotation", "", cron+` | .spec.schedule`))
	assert.Equal(t, "Forbid", renderVelero(t, "platform/secrets-rotation", "", cron+` | .spec.concurrencyPolicy`))
	assert.Equal(t, "true", renderVelero(t, "platform/secrets-rotation", "", cron+` | .spec.suspend`), "production has no image yet")
	assert.Equal(t, "null", renderVelero(t, "platform/secrets-rotation", "dev", cron+` | .spec.suspend`))
	pod := cron + ` | .spec.jobTemplate.spec.template.spec`
	assert.Equal(t, "homelab-lab:dev", renderVelero(t, "platform/secrets-rotation", "dev", pod+` | .containers[0].image`))
	assert.Equal(t, "secrets rotate --scheduled --in-cluster", renderVelero(t, "platform/secrets-rotation", "dev", pod+` | .containers[0].args | join(" ")`))
	assert.Equal(t, "openbao", renderVelero(t, "platform/secrets-rotation", "dev", pod+` | .volumes[] | select(.name == "openbao-token") | .projected.sources[0].serviceAccountToken.audience`))

	roles := renderVelero(t, "platform/secrets-rotation", "", `select(.kind == "Role") | .metadata.namespace`)
	// Not argocd: Forgejo's tokens rotate from the workstation
	assert.ElementsMatch(t, []string{"secrets-rotation", "openbao", "velero", "monitoring-system", "ntfy", "woodpecker", "zot"}, strings.Fields(roles))
	exec := renderVelero(t, "platform/secrets-rotation", "", `select(.kind == "Role" and (.rules[].resources[] == "pods/exec")) | .metadata.namespace`)
	assert.Equal(t, "ntfy", exec)
	claims := renderVelero(t, "platform/secrets-rotation", "", `select(.kind == "Role" and (.rules[].resources[] == "persistentvolumeclaims")) | .metadata.namespace`)
	assert.Equal(t, "woodpecker", claims)
	// Only named objects are changed: Alertmanager's namespace allows refreshing one ExternalSecret and nothing else
	assert.Equal(t, "externalsecrets ntfy", renderVelero(t, "platform/secrets-rotation", "", `select(.kind == "Role" and .metadata.namespace == "monitoring-system") | .rules[] | .resources[0] + " " + (.resourceNames | join(","))`))
	assert.Equal(t, "agent-config-woodpecker-agent-0,agent-config-woodpecker-agent-1", renderVelero(t, "platform/secrets-rotation", "", `select(.kind == "Role" and .metadata.namespace == "woodpecker") | .rules[] | select(.resources[0] == "persistentvolumeclaims") | .resourceNames | join(",")`))
	// A new cluster is not overdue: the CronJob counts from its creation until its first success
	assert.Contains(t, renderVelero(t, "platform/secrets-rotation", "", `select(.kind == "PrometheusRule") | .spec.groups[0].rules[] | select(.alert == "SecretRotationOverdue") | .expr`), "kube_cronjob_created")
	assert.Equal(t, "production", renderVelero(t, "platform/secrets-rotation", "", pod+` | .containers[0].env[] | select(.name == "HOMELAB_ENV") | .value`))
	assert.Equal(t, "sandbox", renderVelero(t, "platform/secrets-rotation", "dev", pod+` | .containers[0].env[] | select(.name == "HOMELAB_ENV") | .value`))
	alerts := renderVelero(t, "platform/secrets-rotation", "", `select(.kind == "PrometheusRule") | .spec.groups[0].rules[].alert`)
	assert.Equal(t, []string{"SecretRotationFailed", "SecretRotationOverdue"}, strings.Fields(alerts))
}
