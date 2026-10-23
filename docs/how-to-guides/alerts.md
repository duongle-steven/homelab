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

## Heartbeat

The `Watchdog` alert fires all the time. Each heartbeat replaces a message scheduled 15 minutes ahead ("Monitoring
heartbeat missing"): if Prometheus or Alertmanager stop, it is delivered. If ntfy, the Tunnel or the whole cluster
stop, nothing is: an external dead man's switch is an idea in `CLAUDE.md`.

## Backups

`VeleroNoRecentBackup` fires when no daily off-site copy succeeded for 26 hours: the schedule fails, or the cluster
was never enabled (every new cluster starts read-only: `lab backup enable`, [Backup and restore](backup-and-restore.md)).
