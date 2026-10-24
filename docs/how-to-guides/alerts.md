# Alerts

Alertmanager posts alerts to ntfy (`apps/ntfy.yaml`), topic `alerts`, published at `https://ntfy.<domain>` through the
Cloudflare Tunnel. Nothing is readable or writable without a user: `phone` reads, `alerts` (Alertmanager) publishes.

## Subscribe on a phone

1. Install ntfy ([Android](https://play.google.com/store/apps/details?id=io.heckel.ntfy) or
   [iOS](https://apps.apple.com/app/ntfy/id1625396347)).
2. Settings → Users → add server `https://ntfy.<domain>`, user `phone`, password:
   `secret/ntfy/generated/phone-password`, property `password`, read from OpenBao with
   [the admin role](../concepts/secrets-management.md#reading-a-generated-value)
3. Subscribe to topic `alerts` on that server.

iOS gets instant notifications through ntfy.sh, which only receives a poll request (a hash of the topic), never the
message. The sandbox runs ntfy without it.

## Telegram, the second path (production)

Every alert also goes to Telegram, so it still arrives while ntfy is down and the cluster is up (a cluster that is
down entirely sends nothing on either path). Alertmanager reads the bot token and the chat id from OpenBao
(`secret/monitoring-system/telegram`), never from git: the chat id names a person's chat.

1. In Telegram, ask [@BotFather](https://t.me/BotFather) for a new bot (`/newbot`): it answers with the bot's token.
2. Send the bot any message, then read the chat id from
   `https://api.telegram.org/bot<token>/getUpdates` (`message.chat.id`).
3. Open OpenBao with the admin role ([Emergency access](../concepts/secrets-management.md#emergency-access)) and
   write both without them showing:

   ```sh
   bao kv put secret/monitoring-system/telegram bot_token=-    # paste the token, then Ctrl-D
   bao kv patch secret/monitoring-system/telegram chat_id=-    # paste the chat id, then Ctrl-D
   ```

Until then Alertmanager runs without it and reports the failed Telegram notifications
(`AlertmanagerFailedToSendAlerts`). A new token: `lab secrets rotate telegram.bot-token` gives the steps
(BotFather's `/revoke` ends the old one at once).

## Heartbeat

The `Watchdog` alert fires all the time. Each heartbeat replaces a message scheduled 15 minutes ahead ("Monitoring
heartbeat missing"): if Prometheus or Alertmanager stop, it is delivered. If ntfy, the Tunnel or the whole cluster
stop, nothing is: an external dead man's switch is an idea in `CLAUDE.md`.

## Backups

`VeleroNoRecentBackup` fires when no daily off-site copy succeeded for 26 hours: the schedule fails, or the cluster
was never enabled (every new cluster starts read-only: `lab backup enable`, [Backup and restore](backup-and-restore.md)).
