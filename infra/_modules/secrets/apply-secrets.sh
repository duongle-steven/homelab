#!/bin/sh
# Applies the Secrets OpenTofu rendered (a v1 List). They come through the environment and reach kubectl on stdin:
# never in an argument or a file
set -eu
: "${KUBECONFIG:?}" "${MANIFEST:?}"
printf '%s' "$MANIFEST" | kubectl apply --server-side --field-manager=infra-secrets -f - >/dev/null
echo "Secrets applied"
