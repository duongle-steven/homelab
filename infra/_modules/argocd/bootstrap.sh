#!/bin/sh
# Render system/argocd and apply it once; Argo CD manages itself from then on (system/argocd is in the root
# ApplicationSet), so the field manager is Argo CD's.
# Env: KUBECONFIG, CHART (system/argocd), VALUES (file name in CHART), SEED (file used while Forgejo is not running;
# production's first install syncs from GitHub until Forgejo holds the repository)
set -eu
kubectl create namespace argocd --dry-run=client -o yaml | kubectl apply -f - >/dev/null
values="$VALUES"
if [ -n "${SEED:-}" ] && [ -z "$(kubectl get pods --all-namespaces -l app.kubernetes.io/instance=forgejo \
  --field-selector status.phase=Running -o name)" ]; then
  values="$SEED"
fi
# Chart.lock is not in git: resolve the dependencies like scripts/render-chart
helm dependency update "$CHART" >/dev/null
manifests=$(mktemp)
trap 'rm -f "$manifests"' EXIT
helm template argocd "$CHART" --namespace argocd --include-crds -f "$CHART/$values" > "$manifests"
apply() { kubectl apply --server-side --field-manager argocd-controller --force-conflicts -f -; }
# The ApplicationSets need Argo CD's CRDs established first
yq 'select(.kind == "CustomResourceDefinition")' "$manifests" | apply
kubectl wait --for condition=Established crd --all --timeout=120s >/dev/null
yq 'select(.kind != "CustomResourceDefinition")' "$manifests" | apply
