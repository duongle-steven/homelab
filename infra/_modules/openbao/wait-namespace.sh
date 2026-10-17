#!/bin/sh
# Waits for OpenBao's namespace, which Argo CD creates when it syncs platform/openbao
set -eu
: "${KUBECONFIG:?}" "${NAMESPACE:?}"
if ! kubectl wait --for=create "namespace/$NAMESPACE" --timeout=30m >/dev/null; then
  echo "namespace $NAMESPACE did not appear in 30 minutes: is Argo CD syncing platform/openbao?" >&2
  exit 1
fi
