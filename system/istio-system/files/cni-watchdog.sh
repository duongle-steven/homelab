#!/bin/sh
# Restarts a node's istio-cni when that node's ztunnel has been running but not Ready for NOT_READY_SECONDS: after a
# node restart, istio-cni's snapshot can hold network namespaces of pod sandboxes that were torn down, and ztunnel
# then retries them forever; a new istio-cni builds its snapshot again (docs/reference/architecture/networking.md).
# ztunnel without a start time (waiting, crash-looping) is left alone: its age is unknown.
set -eu
limit=${NOT_READY_SECONDS:-180}
now=$(date -u +%s)

# GNU date parses RFC 3339 with -d; BusyBox (the image) needs the format
epoch() {
	date -u -d "$1" +%s 2>/dev/null || date -u -D '%Y-%m-%dT%H:%M:%SZ' -d "$1" +%s
}

kubectl -n istio-system get pods -l app=ztunnel -o jsonpath='{range .items[*]}{.spec.nodeName} {.status.containerStatuses[0].ready} {.status.containerStatuses[0].state.running.startedAt}{"\n"}{end}' |
	while read -r node ready started; do
		[ "$ready" = false ] && [ -n "${started:-}" ] || continue
		# An unparsable time skips this node only
		since=$(epoch "$started") || { echo "$node: cannot parse ztunnel's start time $started"; continue; }
		[ $((now - since)) -gt "$limit" ] || continue
		cni=$(kubectl -n istio-system get pods -l k8s-app=istio-cni-node --field-selector "spec.nodeName=$node" -o jsonpath='{range .items[*]}{.metadata.name} {.status.containerStatuses[0].state.running.startedAt}{"\n"}{end}') || cni=
		# shellcheck disable=SC2086 # split into the pod name and its start time
		set -- $cni
		if [ $# -ne 2 ]; then
			echo "$node: ztunnel not Ready, no running istio-cni pod"
			continue
		fi
		cni_since=$(epoch "$2") || { echo "$node: cannot parse istio-cni's start time $2"; continue; }
		if [ $((now - cni_since)) -le "$limit" ]; then
			echo "$node: ztunnel not Ready, istio-cni $1 started less than ${limit}s ago"
			continue
		fi
		echo "$node: ztunnel not Ready for more than ${limit}s, restarting istio-cni $1"
		kubectl -n istio-system delete pod "$1" --wait=false
	done
