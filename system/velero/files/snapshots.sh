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

kubectl get volumesnapshots.snapshot.storage.k8s.io -A -l homelab/snapshot=hourly \
	-o jsonpath='{range .items[*]}{.metadata.namespace} {.metadata.name} {.metadata.creationTimestamp}{"\n"}{end}' |
	awk -v cutoff="$cutoff" '$3 != "" && $3 < cutoff { print $1, $2 }' |
	while read -r ns name; do
		kubectl -n "$ns" delete volumesnapshots.snapshot.storage.k8s.io "$name" --wait=false
	done

if [ -n "$failed" ]; then
	echo "no snapshot of: $(echo "$failed" | tr "\n" " ")" >&2
	exit 1
fi
