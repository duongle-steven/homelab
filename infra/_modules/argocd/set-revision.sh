#!/bin/sh
# Point every source and generator of the homelab repository in rendered Argo CD manifests (stdin) at one revision
# (stdout): the sandbox on GitHub Actions tests a commit without pushing branch sandbox. The repository is the root
# ApplicationSet's first git generator's; other repositories (app-template's chart) keep theirs. No revision: unchanged.
set -eu
revision=${1:-}
if [ -z "$revision" ]; then
	exec cat
fi
manifests=$(cat)
repo=$(printf '%s\n' "$manifests" | yq 'select(.kind == "ApplicationSet" and .metadata.name == "root") | .spec.generators[0].matrix.generators[0].git.repoURL')
[ -n "$repo" ] && [ "$repo" != null ] || { echo "set-revision: no root ApplicationSet git generator" >&2; exit 1; }
printf '%s\n' "$manifests" | REPO="$repo" REVISION="$revision" yq '
  (.. | select(tag == "!!map" and .repoURL == strenv(REPO) and has("revision")) | .revision) = strenv(REVISION) |
  (.. | select(tag == "!!map" and .repoURL == strenv(REPO) and has("targetRevision")) | .targetRevision) = strenv(REVISION)'
