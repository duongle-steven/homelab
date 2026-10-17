#!/bin/sh
# Writes OpenBao's unseal key into its namespace (wait-namespace.sh waited for it). The key comes in base64 through
# the environment and reaches kubectl on stdin: never in an argument or a file
set -eu
: "${KUBECONFIG:?}" "${NAMESPACE:?}" "${KEY:?}"
printf '%s' "$KEY" | base64 -d |
  kubectl -n "$NAMESPACE" create secret generic openbao-unseal --from-file=key=/dev/stdin --dry-run=client -o yaml |
  kubectl apply --server-side --field-manager=infra-openbao -f - >/dev/null
echo "Secret $NAMESPACE/openbao-unseal applied"
