package offline

import (
	"strings"
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

// prometheus-operator starts a controller only for the CRDs it finds when it starts, and never later: a fresh cluster
// whose operator came up before the Prometheus and Alertmanager CRDs were established never made either (the first
// release's sandbox, 2026-11-13). Argo CD's sync waves order them: the CRDs and the rest (wave 0, healthy once
// established), the operator (1), the resources it serves (2); Telegram's ExternalSecret last (3), since it stays
// unhealthy until a person writes the bot and a wave waits for every resource before it to be healthy
func TestOperatorAfterItsCRDs(t *testing.T) {
	for _, env := range []string{"", "dev"} {
		waves := renderVelero(t, "system/monitoring-system", env, `select(.metadata.annotations."argocd.argoproj.io/sync-wave" != null) | .kind + "/" + .metadata.name + "=" + .metadata.annotations."argocd.argoproj.io/sync-wave"`)
		want := "Deployment/monitoring-system-kube-pro-operator=1\nAlertmanager/monitoring-system-kube-pro-alertmanager=2\nPrometheus/monitoring-system-kube-pro-prometheus=2"
		if env == "" {
			want += "\nExternalSecret/telegram=3"
		}
		assert.ElementsMatch(t, strings.Split(want, "\n"), strings.Split(waves, "\n"), "env %q", env)
	}
}
