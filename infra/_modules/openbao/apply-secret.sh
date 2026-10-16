#!/bin/sh
# Writes OpenBao's unseal key into its namespace once Argo CD has created it. The key comes in base64 through the
# environment and reaches kubectl on stdin: never in an argument or a file
set -eu
: "${KUBECONFIG:?}" "${NAMESPACE:?}" "${KEY:?}"
if ! kubectl wait --for=create "namespace/$NAMESPACE" --timeout=30m >/dev/null; then
  echo "namespace $NAMESPACE did not appear in 30 minutes: is Argo CD syncing platform/openbao?" >&2
  exit 1
fi
printf '%s' "$KEY" | base64 -d |
  kubectl -n "$NAMESPACE" create secret generic openbao-unseal --from-file=key=/dev/stdin --dry-run=client -o yaml |
  kubectl apply --server-side --field-manager=infra-openbao -f - >/dev/null
echo "Secret $NAMESPACE/openbao-unseal applied"
