#!/bin/sh
# Point a render's Forgejo repository (ops/homelab) at the seed repository on one branch (stdin to stdout): staging
# syncs GitHub branch staging, which make staging pushes, until Forgejo holds ops/homelab. Other repositories (the
# app-template chart) keep theirs. No branch: unchanged (production's seed has its own values file).
# Usage: set-seed.sh <seed repository> <branch>
set -eu
seed=$1
branch=${2:-}
if [ -z "$branch" ]; then
	exec cat
fi
[ -n "$seed" ] || { echo "set-seed.sh: no seed repository (config.json's seed_repository)" >&2; exit 1; }
forgejo=http://forgejo-http.forgejo:3000/ops/homelab
SEED="$seed" BRANCH="$branch" FORGEJO="$forgejo" yq '
  (.. | select(tag == "!!map" and .repoURL == strenv(FORGEJO) and has("revision")) | .revision) = strenv(BRANCH) |
  (.. | select(tag == "!!map" and .repoURL == strenv(FORGEJO) and has("targetRevision")) | .targetRevision) = strenv(BRANCH) |
  (.. | select(tag == "!!map" and .repoURL == strenv(FORGEJO)) | .repoURL) = strenv(SEED)'
