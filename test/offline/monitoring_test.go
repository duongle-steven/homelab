package offline

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Telegram is the second path for every alert in production (ntfy down, cluster up): the receiver ntfy also sends to
// Telegram, with the bot token and the chat id read from files mounted from OpenBao, never in git; the sandbox has
// no bot (step 11b)
func TestAlertmanagerTelegramProductionOnly(t *testing.T) {
	const config = `select(.kind == "Secret" and .metadata.name == "alertmanager-monitoring-system-kube-pro-alertmanager") | .data."alertmanager.yaml" | @base64d | from_yaml`
	telegram := renderVelero(t, "system/monitoring-system", "", config+` | .receivers[] | select(.name == "ntfy") | .telegram_configs[0] | .bot_token_file + " " + .chat_id_file`)
	assert.Equal(t, "/etc/alertmanager/secrets/telegram/bot-token /etc/alertmanager/secrets/telegram/chat-id", telegram)
	assert.Equal(t, "null", renderVelero(t, "system/monitoring-system", "", config+` | .receivers[] | select(.name == "ntfy-watchdog") | .telegram_configs`), "the heartbeat stays on ntfy")
	// Optional: Alertmanager starts before a person writes the bot to OpenBao
	assert.Equal(t, "telegram true", renderVelero(t, "system/monitoring-system", "", `select(.kind == "Alertmanager") | .spec.volumes[] | select(.name == "telegram") | .secret.secretName + " " + (.secret.optional | tostring)`))
	assert.Equal(t, "monitoring-system/telegram", renderVelero(t, "system/monitoring-system", "", `select(.kind == "ExternalSecret" and .metadata.name == "telegram") | .spec.data[0].remoteRef.key`))

	assert.Equal(t, "null", renderVelero(t, "system/monitoring-system", "dev", config+` | .receivers[] | select(.name == "ntfy") | .telegram_configs`))
	assert.Empty(t, renderVelero(t, "system/monitoring-system", "dev", `select(.kind == "Alertmanager") | .spec.volumes[]? | .name`))
	assert.Empty(t, renderVelero(t, "system/monitoring-system", "dev", `select(.kind == "ExternalSecret" and .metadata.name == "telegram") | .metadata.name`))
}

// values-local.yaml restates Alertmanager's receivers without Telegram (Helm replaces lists): they must stay the same
// otherwise
func TestAlertmanagerDevReceiversFollowProduction(t *testing.T) {
	const config = `select(.kind == "Secret" and .metadata.name == "alertmanager-monitoring-system-kube-pro-alertmanager") | .data."alertmanager.yaml" | @base64d | from_yaml`
	prod := renderVelero(t, "system/monitoring-system", "", config+` | .receivers | map(del(.telegram_configs)) | to_json`)
	dev := renderVelero(t, "system/monitoring-system", "dev", config+` | .receivers | to_json`)
	assert.JSONEq(t, prod, dev, "system/monitoring-system/values-local.yaml's receivers differ from values.yaml's beyond Telegram")
}
