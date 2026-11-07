#!/bin/sh
# Clears a node whose mesh is stuck (docs/reference/architecture/networking.md). After a node restart, istio-cni's
# snapshot can hold network namespaces of pod sandboxes that were torn down, and ztunnel then retries them forever;
# a new istio-cni builds its snapshot again. A node is stuck when, for more than NOT_READY_SECONDS:
# - its ztunnel has been running but not Ready, or
# - a pod on it has stayed Pending while istio-cni's add keeps failing for it (FailedCreatePodSandBox): ztunnel can
#   report Ready while still retrying dead network namespaces.
# The watchdog restarts the node's istio-cni, or its ztunnel when istio-cni was already restarted after ztunnel's start
# and the node is still stuck; nothing while either started less than NOT_READY_SECONDS ago. The pods' start times are
# the only state. ztunnel without a start time (waiting, crash-looping) is left alone: its age is unknown.
set -eu
limit=${NOT_READY_SECONDS:-180}
now=$(date -u +%s)

# GNU date parses RFC 3339 with -d; BusyBox (the image) needs the format
epoch() {
	date -u -d "$1" +%s 2>/dev/null || date -u -D '%Y-%m-%dT%H:%M:%SZ' -d "$1" +%s
}

# Nodes with a pod Pending for more than the limit whose sandbox istio-cni failed to add within the limit
stuck_nodes=$(
	kubectl get events -A --field-selector reason=FailedCreatePodSandBox -o jsonpath='{range .items[*]}{.involvedObject.namespace}/{.involvedObject.name} {.lastTimestamp} {.message}{"\n"}{end}' |
		while read -r pod seen message; do
			case "$message" in *'type="istio-cni"'*'failed (add)'*) ;; *) continue ;; esac
			seen=$(epoch "$seen" 2>/dev/null) || continue
			[ $((now - seen)) -le "$limit" ] || continue
			# shellcheck disable=SC2046 # split into the node, the phase and the creation time
			set -- $(kubectl -n "${pod%%/*}" get pod "${pod#*/}" -o jsonpath='{.spec.nodeName} {.status.phase} {.metadata.creationTimestamp}' 2>/dev/null || true)
			[ $# -eq 3 ] && [ "$2" = Pending ] || continue
			created=$(epoch "$3" 2>/dev/null) || continue
			[ $((now - created)) -gt "$limit" ] && echo "$1"
		done | sort -u
)

kubectl -n istio-system get pods -l app=ztunnel -o jsonpath='{range .items[*]}{.metadata.name} {.spec.nodeName} {.status.containerStatuses[0].ready} {.status.containerStatuses[0].state.running.startedAt}{"\n"}{end}' |
	while read -r ztunnel node ready started; do
		[ -n "${started:-}" ] || continue
		if [ "$ready" = false ]; then
			why="ztunnel not Ready"
		elif printf '%s\n' "$stuck_nodes" | grep -qx "$node"; then
			why="pods stuck behind istio-cni's add"
		else
			continue
		fi
		# An unparsable time skips this node only
		since=$(epoch "$started") || { echo "$node: cannot parse ztunnel's start time $started"; continue; }
		[ $((now - since)) -gt "$limit" ] || continue
		cni=$(kubectl -n istio-system get pods -l k8s-app=istio-cni-node --field-selector "spec.nodeName=$node" -o jsonpath='{range .items[*]}{.metadata.name} {.status.containerStatuses[0].state.running.startedAt}{"\n"}{end}') || cni=
		# shellcheck disable=SC2086 # split into the pod name and its start time
		set -- $cni
		if [ $# -ne 2 ]; then
			echo "$node: $why, no running istio-cni pod"
			continue
		fi
		cni_since=$(epoch "$2") || { echo "$node: cannot parse istio-cni's start time $2"; continue; }
		if [ $((now - cni_since)) -le "$limit" ]; then
			echo "$node: $why, istio-cni $1 started less than ${limit}s ago"
			continue
		fi
		if [ "$cni_since" -gt "$since" ]; then
			echo "$node: $why for more than ${limit}s after istio-cni's restart, restarting ztunnel $ztunnel"
			kubectl -n istio-system delete pod "$ztunnel" --wait=false
		else
			echo "$node: $why for more than ${limit}s, restarting istio-cni $1"
			kubectl -n istio-system delete pod "$1" --wait=false
		fi
	done
