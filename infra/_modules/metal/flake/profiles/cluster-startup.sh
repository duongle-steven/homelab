#!/usr/bin/env bash
# Undoes lab cluster shutdown, from its marker (cli/internal/cluster/shutdown.go writes it). Every node's unit runs it at
# boot, and lab cluster startup with --force. Without a marker there is nothing to do: a sudden cut recovers by itself.
# Every step can run twice and on three nodes at once. Exit 75 asks the unit to try again.
set -euo pipefail

k3s_yaml="${K3S_YAML:-/etc/rancher/k3s/k3s.yaml}"
retry_seconds="${RETRY_SECONDS:-15}"
noout_limit="${NOOUT_LIMIT:-1800}"
ceph_config=/var/lib/rook/rook-ceph/rook-ceph.config
force=false
[ "${1:-}" != --force ] || force=true

# A joiner not yet enrolled: no k3s to ask, and none until lab enrolls it
[ -f "$k3s_yaml" ] || exit 0
export KUBECONFIG="$k3s_yaml"

kube() {
	if [ -n "${KUBECTL:-}" ]; then
		"$KUBECTL" "$@"
	else
		k3s kubectl "$@"
	fi
}

not_found=3
no_resource=4
errors=$(mktemp)
trap 'rm -f "$errors"' EXIT

# ask runs kubectl until the API answers, however long the other nodes take: 0 with its output, or the code above
ask() {
	local out
	while :; do
		if out=$(kube "$@" 2>"$errors"); then
			printf '%s\n' "$out"
			return 0
		fi
		if grep -q NotFound "$errors"; then
			return "$not_found"
		fi
		if grep -q "doesn't have a resource type" "$errors"; then
			return "$no_resource"
		fi
		echo "waiting for the API: $(head -c 200 "$errors")" >&2
		sleep "$retry_seconds"
	done
}

# gone_ok: another node finished, or what the marker names left git meanwhile
gone_ok() {
	local code=0
	ask "$@" >/dev/null || code=$?
	[ "$code" = 0 ] || [ "$code" = "$not_found" ]
}

# marker sets the variable $1 to the marker's key $2, or ends here: without a marker another node finished
marker() {
	local code=0 value
	value=$(ask -n kube-system get configmap cluster-shutdown -o "jsonpath={.data.$2}") || code=$?
	if [ "$code" = "$not_found" ]; then
		exit 0
	fi
	[ "$code" = 0 ]
	printf -v "$1" '%s' "$value"
}

boots="" began="" crons="" replicas=""
marker boots boots
if [ "$force" = false ]; then
	# One node rebooting during a shutdown must not resume what it stops; a cut halfway through reboots them all
	total=0
	rebooted=0
	for line in $boots; do
		total=$((total + 1))
		code=0
		now=$(ask get node "${line%%=*}" -o 'jsonpath={.status.nodeInfo.bootID}') || code=$?
		if [ "$code" = 0 ] && [ "$now" != "${line#*=}" ]; then
			rebooted=$((rebooted + 1))
		fi
	done
	if [ $((rebooted * 2)) -le "$total" ]; then
		echo "$rebooted of $total nodes on a new boot: waiting for a majority"
		exit 75
	fi
fi

# The limit below counts from the first start, whichever node or restart runs this now
marker began startupBegan
if [ -z "$began" ]; then
	began=$(date +%s)
	gone_ok -n kube-system patch configmap cluster-shutdown --type merge -p "{\"data\":{\"startupBegan\":\"$began\"}}"
fi

marker crons cronjobs
for cron in $crons; do
	echo "resuming $cron"
	gone_ok -n "${cron%%/*}" patch cronjob "${cron#*/}" --type merge -p '{"spec":{"suspend":false}}'
done
marker replicas replicas
echo "starting Argo CD's application controller"
gone_ok -n argocd scale statefulset argocd-application-controller "--replicas=$replicas"

code=0
ask get cephclusters.ceph.rook.io -A -o name >/dev/null || code=$?
if [ "$code" = 0 ]; then
	while :; do
		osds=$(ask -n rook-ceph get pods -l app=rook-ceph-osd \
			-o 'jsonpath={range .items[*]}{.status.conditions[?(@.type=="Ready")].status} {end}') || osds=""
		if [ -n "${osds// /}" ] && ! grep -q False <<<"$osds"; then
			break
		fi
		# Short of a node for good: Ceph must copy what it held rather than stay short of a replica
		if [ $(($(date +%s) - began)) -ge "$noout_limit" ]; then
			echo "an OSD is still away after ${noout_limit} s: letting Ceph recover without it"
			break
		fi
		sleep "$retry_seconds"
	done
	gone_ok -n rook-ceph exec deploy/rook-ceph-operator -- ceph "--conf=$ceph_config" osd unset noout
fi

code=0
postgres=$(ask get clusters.postgresql.cnpg.io -A -o 'jsonpath={range .items[*]}{.metadata.namespace}/{.metadata.name}={.status.phase}{"\n"}{end}') || code=$?
if [ "$code" = 0 ] && [ -n "$postgres" ]; then
	# Reported, never waited for: its Clusters must not hold backups and rotation off
	echo "CloudNativePG: $postgres"
fi

gone_ok -n kube-system delete configmap cluster-shutdown --ignore-not-found
echo "the shutdown is undone"
