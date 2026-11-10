#!/bin/sh
# Argo CD's in-cluster cluster Secret, carrying what OpenTofu knows for the ApplicationSets (the GitOps Bridge pattern):
# their clusters generators select it by homelab/bootstrap and read its annotations with dot access, so a missing one
# fails instead of rendering empty. Every annotation is always written. Nothing deletes it: unmatched, every
# ApplicationSet would delete its Applications.
# Env: REPOSITORY (required: the homelab repository Argo CD syncs), BRANCH (required: sandbox, testbed or master),
# REVISION (optional: a commit overriding BRANCH, just dev --revision), BACKUP_S3_URL (optional: production's R2
# endpoint), OPTIONAL_APPS (optional, space-separated: the sandbox's optional apps, one label each)
set -eu
: "${REPOSITORY:?REPOSITORY is required}" "${BRANCH:?BRANCH is required}"
# shellcheck disable=SC2086 # one app per word
apps=$(printf '%s\n' ${OPTIONAL_APPS:-} | sort -u | jq -R . | jq -s .)
jq -n --arg repository "$REPOSITORY" --arg revision "${REVISION:-$BRANCH}" --arg backup "${BACKUP_S3_URL:-}" \
	--argjson apps "$apps" '{
  apiVersion: "v1",
  kind: "Secret",
  metadata: {
    name: "in-cluster",
    namespace: "argocd",
    labels: ({"argocd.argoproj.io/secret-type": "cluster", "homelab/bootstrap": "true"}
      + ([$apps[] | select(. != "") | {key: ("homelab/app-" + .), value: "true"}] | from_entries)),
    annotations: {repository: $repository, revision: $revision, backupS3Url: $backup}
  },
  stringData: {name: "in-cluster", server: "https://kubernetes.default.svc"}
}' | yq -P
