#!/bin/sh
# Which values file the Argo CD bootstrap applies (OpenTofu's external data source: a JSON query on stdin, a JSON object
# on stdout). Without a seed it is the given one. With a seed (production's first install syncs the seed repository on
# GitHub until Forgejo holds ops/homelab), the seed only while Forgejo does not run and Argo CD has never read Forgejo:
# once switched, a Forgejo restart must not send production back to the seed. A cluster that cannot be asked fails.
set -eu
query=$(cat)
get() { printf '%s' "$query" | jq -r --arg k "$1" '.[$k] // ""'; }
values=$(get values)
environment=$(get environment)
seed=$(get seed)
if [ -n "$seed" ]; then
	KUBECONFIG=$(get kubeconfig)
	export KUBECONFIG
	root=$(kubectl -n argocd get applicationset root --ignore-not-found \
		-o jsonpath='{.spec.generators[0].matrix.generators[0].git.repoURL}')
	if [ "$root" != "$(get forgejo_repository)" ]; then
		running=$(kubectl get pods --all-namespaces -l app.kubernetes.io/instance=forgejo \
			--field-selector status.phase=Running -o name)
		if [ -z "$running" ]; then
			values=$seed
			environment=seed
		fi
	fi
fi
jq -n --arg values "$values" --arg environment "$environment" '{values: $values, environment: $environment}'
