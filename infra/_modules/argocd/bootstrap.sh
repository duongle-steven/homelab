#!/bin/sh
# Render system/argocd and apply it (again whenever infra/_modules/argocd sees a change): Argo CD does not manage
# itself. The field manager stays Argo CD's, which managed it before step 10b.
# Env: KUBECONFIG, CHART (system/argocd), VALUES (file name in CHART, chosen by choose-values.sh: the seed on
# production's first install), REVISION (optional: the commit every homelab source syncs, set-revision.sh),
# ENVIRONMENT (prod, dev or seed, for scripts/env-values: the chart gets the same global values Argo CD passes it)
set -eu
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f - >/dev/null
# shellcheck disable=SC2153 # VALUES and ENVIRONMENT come from OpenTofu (set -u fails when one is missing)
values="$VALUES"
# shellcheck disable=SC2153
environment="$ENVIRONMENT"
# Chart.lock is not in git: resolve the dependencies like scripts/render-chart
helm dependency update "$CHART" >/dev/null
manifests=$(mktemp)
trap 'rm -f "$manifests" "$manifests.rev"' EXIT
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
# The seed's repository is config.json's seed_repository (values-seed.yaml holds a placeholder)
if [ "$environment" = seed ]; then
  [ -n "${SEED_REPOSITORY:-}" ] || { echo "seed_repository is not set in config.json" >&2; exit 1; }
  sed -i "s|https://seed-repository.invalid|$SEED_REPOSITORY|g" "$manifests"
fi
if grep -q seed-repository.invalid "$manifests"; then
  echo "the seed repository placeholder was not replaced" >&2
  exit 1
fi
# The sandbox on GitHub Actions syncs one commit (REVISION) instead of branch sandbox
sh "$(dirname "$0")/set-revision.sh" "${REVISION:-}" < "$manifests" > "$manifests.rev"
mv "$manifests.rev" "$manifests"
# Production's off-site backup endpoint, which holds R2's account id (infra/production/argocd)
sh "$(dirname "$0")/set-backup-endpoint.sh" "${BACKUP_S3_URL:-}" < "$manifests" > "$manifests.rev"
mv "$manifests.rev" "$manifests"
# Argo CD no longer manages itself: the root ApplicationSet drops Application argocd, whose resources finalizer would
# delete Argo CD with it. Release it first (nothing to do on a new cluster).
if kubectl -n argocd get application argocd >/dev/null 2>&1; then
  kubectl -n argocd patch application argocd --type merge -p '{"metadata":{"finalizers":null}}' >/dev/null
fi
# Objects without a namespace belong to argocd, as when Argo CD syncs this chart
apply() { kubectl apply --namespace argocd --server-side --field-manager argocd-controller --force-conflicts -f -; }
# The ApplicationSets need Argo CD's CRDs established first
yq 'select(.kind == "CustomResourceDefinition")' "$manifests" | apply
kubectl wait --for condition=Established crd --all --timeout=120s >/dev/null
yq 'select(.kind != "CustomResourceDefinition")' "$manifests" | apply
