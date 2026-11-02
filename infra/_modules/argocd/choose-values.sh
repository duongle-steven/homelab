#!/bin/sh
# Which values file the Argo CD bootstrap applies (OpenTofu's external data source: a JSON query on stdin, a JSON object
# on stdout). Without a seed it is the given one. With a seed (production's first install syncs the seed repository on
# GitHub until Forgejo holds ops/homelab), the seed only until Argo CD's own credential reads ops/homelab's master on
# Forgejo, and never again once Argo CD reads Forgejo: a Forgejo restart must not send production back to the seed. A
# cluster that cannot be asked fails.
set -eu
query=$(cat)
get() { printf '%s' "$query" | jq -r --arg k "$1" '.[$k] // ""'; }
values=$(get values)
environment=$(get environment)
seed=$(get seed)
seeded=false
if [ -n "$seed" ]; then
	KUBECONFIG=$(get kubeconfig)
	export KUBECONFIG
	# A new cluster has no Argo CD yet (no ApplicationSet kind): a first install, not a cluster that cannot be asked
	if ! root=$(kubectl -n argocd get applicationset root --ignore-not-found \
		-o jsonpath='{.spec.generators[0].matrix.generators[0].git.repoURL}' 2>"${TMPDIR:-/tmp}/choose-values.$$"); then
		if ! grep -q "doesn't have a resource type" "${TMPDIR:-/tmp}/choose-values.$$"; then
			cat "${TMPDIR:-/tmp}/choose-values.$$" >&2
			rm -f "${TMPDIR:-/tmp}/choose-values.$$"
			exit 1
		fi
		root=
	fi
	rm -f "${TMPDIR:-/tmp}/choose-values.$$"
	if [ "$root" != "$(get forgejo_repository)" ]; then
		# Argo CD's own credential (lab infra forgejo sync's read-only token) reads ops/homelab's master: Forgejo
		# runs, the token exists and lab infra forgejo push deployed. The token goes through stdin, not an argument.
		token=$(kubectl -n argocd get secret argocd-repo-forgejo --ignore-not-found -o jsonpath='{.data.password}')
		# curl reads the header from stdin (-H @-), so the token is in no process's arguments
		if [ -z "$token" ] || ! printf 'Authorization: token %s\n' "$(printf '%s' "$token" | base64 -d)" |
			kubectl -n forgejo exec -i deploy/forgejo -c forgejo -- \
			curl -sf -o /dev/null -H @- http://localhost:3000/api/v1/repos/ops/homelab/branches/master >/dev/null 2>&1; then
			values=$seed
			# Production's seed has its own file and environment; testbed seeds from its own values under its own
			# environment (set-seed.sh then points it at the seed branch)
			environment=$(get seed_environment)
			environment=${environment:-seed}
			seeded=true
		fi
	fi
fi
jq -n --arg values "$values" --arg environment "$environment" --arg seeded "$seeded" \
	'{values: $values, environment: $environment, seeded: $seeded}'
