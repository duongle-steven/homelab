#!/bin/sh
# Keeps the newest KEEP completed backups of each schedule and of each app (lab backup push labels them homelab/app)
# and deletes the older ones; a failed backup is never counted and goes once a newer one of its group completed.
# Retention is by count, never by age, and runs only while location default is ReadWrite: a cluster that was down for
# months, or a fresh one before lab backup enable, deletes nothing (docs/how-to-guides/backup-and-restore.md).
set -eu
keep=${KEEP:-30}
ns=${NAMESPACE:-velero}

mode=$(kubectl -n "$ns" get backupstoragelocations.velero.io default -o jsonpath='{.spec.accessMode}')
if [ "$mode" != ReadWrite ]; then
	echo "location default is ${mode:-missing}: nothing pruned"
	exit 0
fi

# Oldest first (RFC 3339 times sort as text); the script walks each group from its newest
kubectl -n "$ns" get backups.velero.io --sort-by=.metadata.creationTimestamp \
	-o jsonpath='{range .items[*]}{.metadata.name}|{.metadata.labels.velero\.io/schedule-name}|{.metadata.labels.homelab/app}|{.status.phase}{"\n"}{end}' |
	awk -F'|' -v keep="$keep" '
		$1 == "" { next }
		{
			group = $2 != "" ? "schedule " $2 : ($3 != "" ? "app " $3 : "")
			if (group == "") next
			n[group]++
			name[group, n[group]] = $1
			phase[group, n[group]] = $4
		}
		END {
			for (g in n) {
				completed = 0
				for (i = n[g]; i >= 1; i--) {
					if (phase[g, i] == "Completed") {
						completed++
						if (completed > keep) gone[g, i] = 1
					} else if (phase[g, i] ~ /^(Failed|PartiallyFailed|FailedValidation)$/ && completed > 0) {
						gone[g, i] = 1
					}
				}
				# Oldest first
				for (i = 1; i <= n[g]; i++) if ((g, i) in gone) print name[g, i]
			}
		}' |
	while read -r backup; do
		echo "deleting backup $backup"
		kubectl -n "$ns" create -f - <<-DOC
			apiVersion: velero.io/v1
			kind: DeleteBackupRequest
			metadata:
			  generateName: $backup-
			  namespace: $ns
			spec:
			  backupName: $backup
		DOC
	done
