#!/bin/sh
# Render system/argocd and apply it once; Argo CD manages itself from then on (system/argocd is in the root
# ApplicationSet), so the field manager is Argo CD's.
# Env: KUBECONFIG, CHART (system/argocd), VALUES (file name in CHART), SEED (file used while Forgejo is not running;
# production's first install syncs from GitHub until Forgejo holds the repository), ENVIRONMENT (prod or dev, for
# scripts/env-values: the chart gets the same global values Argo CD passes it)
set -eu
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f - >/dev/null
values="$VALUES"
environment="$ENVIRONMENT"
if [ -n "${SEED:-}" ] && [ -z "$(kubectl get pods --all-namespaces -l app.kubernetes.io/instance=forgejo \
  --field-selector status.phase=Running -o name)" ]; then
  values="$SEED"
  environment=seed
fi
# Chart.lock is not in git: resolve the dependencies like scripts/render-chart
helm dependency update "$CHART" >/dev/null
manifests=$(mktemp)
trap 'rm -f "$manifests"' EXIT
# The global values Argo CD passes from infra/<env>/cluster/config.json (addresses, domain, ...)
set --
IFS='
'
for value in $(cd "$CHART/../.." && scripts/env-values "$environment" root); do
  set -- "$@" --set-string "$value"
done
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
# Objects without a namespace belong to argocd, as when Argo CD syncs this chart
apply() { kubectl apply --namespace argocd --server-side --field-manager argocd-controller --force-conflicts -f -; }
# The ApplicationSets need Argo CD's CRDs established first
yq 'select(.kind == "CustomResourceDefinition")' "$manifests" | apply
kubectl wait --for condition=Established crd --all --timeout=120s >/dev/null
yq 'select(.kind != "CustomResourceDefinition")' "$manifests" | apply
