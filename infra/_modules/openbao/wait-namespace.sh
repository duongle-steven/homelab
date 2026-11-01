#!/bin/sh
# Waits for OpenBao's namespace, which Argo CD creates when it syncs platform/openbao. It asks again until its deadline:
# during a first sync the API server restarts and its VIP moves (kubectl wait stops at the first refused connection)
set -eu
: "${KUBECONFIG:?}" "${NAMESPACE:?}"
deadline=$(($(date +%s) + ${WAIT_SECONDS:-1800}))
until kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "namespace $NAMESPACE did not appear in 30 minutes: is Argo CD syncing platform/openbao?" >&2
    exit 1
  fi
  sleep "${WAIT_INTERVAL:-10}"
done
