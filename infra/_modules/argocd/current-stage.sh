#!/bin/sh
# The stage on the cluster's Argo CD cluster Secret (label homelab/stage), empty when there is no Secret yet (a new
# cluster). A cluster that cannot be asked fails: read as empty, it would look new and move a platform cluster to all
# (12b-2's review). Env: KUBECONFIG
set -eu
secret=$(kubectl -n argocd get secret in-cluster --ignore-not-found -o json)
[ -n "$secret" ] || exit 0
printf '%s' "$secret" | jq -r '.metadata.labels["homelab/stage"] // ""'
