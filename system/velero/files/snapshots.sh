#!/bin/sh
# Hourly local snapshots: a VolumeSnapshot of every bound claim labelled homelab/backup daily or manual, a namespace's
# claims one right after the other (Forgejo's two volumes stay close), then deletes this job's snapshots older than
# SNAPSHOT_HOURS. Nothing here reaches the internet: lab backup restore --from local works offline. A run within the
# same hour finds that hour's snapshots there already; a claim that fails does not stop the others or the cleanup, and
# fails the Job at the end.
set -eu
hours=${SNAPSHOT_HOURS:-48}
stamp=$(date -u +%Y%m%d%H)
# RFC 3339 times sort as text
cutoff=$(date -u -d "@$(($(date -u +%s) - hours * 3600))" +%Y-%m-%dT%H:%M:%SZ)

# The loop prints only the claims it failed to snapshot; everything else goes to stderr
failed=$(kubectl get persistentvolumeclaims -A -l 'homelab/backup in (daily,manual)' \
	-o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.name} {.status.phase}{"\n"}{end}' | sort |
	while read -r ns pvc phase; do
		[ "$phase" = Bound ] || { echo "$ns/$pvc is $phase: skipped" >&2; continue; }
		if ! out=$(kubectl create -f - 2>&1 <<-DOC
			apiVersion: snapshot.storage.k8s.io/v1
			kind: VolumeSnapshot
			metadata:
			  name: $pvc-$stamp
			  namespace: $ns
			  labels:
			    homelab/snapshot: hourly
			    homelab/pvc: $pvc
			spec:
			  source:
			    persistentVolumeClaimName: $pvc
		DOC
		); then
			case "$out" in
				*AlreadyExists*) echo "$ns/$pvc: this hour's snapshot exists" >&2 ;;
				*) echo "$ns/$pvc: $out" >&2; echo "$ns/$pvc" ;;
			esac
		else
			echo "$out" >&2
		fi
	done)

# Older than the cutoff goes, except each claim's newest ready snapshot: if snapshotting breaks, the last good one stays
kubectl get volumesnapshots.snapshot.storage.k8s.io -A -l homelab/snapshot=hourly \
	-o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.name} {.metadata.creationTimestamp} {.status.readyToUse} {.metadata.labels.homelab/pvc}{"\n"}{end}' |
	awk -v cutoff="$cutoff" '
		$3 == "" { next }
		{ ns[NR] = $1; name[NR] = $2; created[NR] = $3 }
		$4 == "true" && $3 > newest[$1 " " $5] { newest[$1 " " $5] = $3; keep[$1 " " $5] = NR }
		END {
			for (k in keep) kept[keep[k]] = 1
			for (i = 1; i <= NR; i++) if ((i in created) && created[i] < cutoff && !(i in kept)) print ns[i], name[i]
		}' |
	while read -r ns name; do
		kubectl -n "$ns" delete volumesnapshots.snapshot.storage.k8s.io "$name" --wait=false
	done

if [ -n "$failed" ]; then
	echo "no snapshot of: $(echo "$failed" | tr "\n" " ")" >&2
	exit 1
fi
