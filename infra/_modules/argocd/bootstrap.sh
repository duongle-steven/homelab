#!/bin/sh
# Render system/argocd and apply it (again whenever infra/_modules/argocd sees a change): Argo CD does not manage
# itself. The field manager stays Argo CD's, which managed it before step 10b.
# Env: KUBECONFIG, CHART (system/argocd), VALUES (file name in CHART, chosen by choose-values.sh: the seed on
# production's first install), ENVIRONMENT (prod, testbed, dev or seed, for scripts/env-values: the chart gets the same
# global values Argo CD passes it), and cluster-secret.sh's REPOSITORY, BRANCH, REVISION, BACKUP_S3_URL and OPTIONAL_APPS:
# what is not in git or known only now reaches the ApplicationSets through the cluster Secret, not a rewrite of the render
set -eu
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f - >/dev/null
# Objects without a namespace belong to argocd, as when Argo CD syncs this chart
apply() { kubectl apply --namespace argocd --server-side --field-manager argocd-controller --force-conflicts -f -; }
# The cluster Secret the ApplicationSets read goes first: until it exists their generators match nothing
sh "$(dirname "$0")/cluster-secret.sh" | apply
# shellcheck disable=SC2153 # VALUES and ENVIRONMENT come from OpenTofu (set -u fails when one is missing)
values="$VALUES"
# shellcheck disable=SC2153
environment="$ENVIRONMENT"
# Chart.lock is not in git: resolve the dependencies like scripts/render-chart
helm dependency update "$CHART" >/dev/null
manifests=$(mktemp)
trap 'rm -f "$manifests"' EXIT
# The global values Argo CD passes from infra/<env>/cluster/config.json (addresses, domain, ...)
# Captured first: set -e ignores a failing command substitution in a for list (a key missing from config.json)
values_from_config=$(cd "$CHART/../.." && scripts/env-values "$environment" root)
set --
IFS='
'
set -f
for value in $values_from_config; do
  set -- "$@" --set-string "$value"
done
set +f
unset IFS
helm template argocd "$CHART" --namespace argocd --include-crds -f "$CHART/$values" "$@" > "$manifests"
# Argo CD no longer manages itself: the root ApplicationSet drops Application argocd, whose resources finalizer would
# delete Argo CD with it. Release it first (nothing to do on a new cluster).
if kubectl -n argocd get application argocd >/dev/null 2>&1; then
  kubectl -n argocd patch application argocd --type merge -p '{"metadata":{"finalizers":null}}' >/dev/null
fi
# The ApplicationSets need Argo CD's CRDs established first
yq 'select(.kind == "CustomResourceDefinition")' "$manifests" | apply
kubectl wait --for condition=Established crd --all --timeout=120s >/dev/null
yq 'select(.kind != "CustomResourceDefinition")' "$manifests" | apply
