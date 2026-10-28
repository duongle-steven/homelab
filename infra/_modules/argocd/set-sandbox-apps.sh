#!/bin/sh
# Add the sandbox's optional apps (paperless, opencloud: deployed only when a change or make dev apps= asks,
# docs/superpowers/specs/2026-10-28-sandbox-optional-apps-design.md) to the apps ApplicationSet's git generator in
# rendered Argo CD manifests (stdin, stdout). Names are space-separated; each is added once. None: unchanged.
set -eu
apps=${1:-}
if [ -z "$(echo "$apps" | tr -d ' ')" ]; then
	exec cat
fi
paths=$(for app in $apps; do echo "apps/$app.yaml"; done | sort -u | tr '\n' ' ')
PATHS="$paths" yq '
  (select(.kind == "ApplicationSet" and .metadata.name == "apps") | .spec.generators[0].matrix.generators[0].git.files) |=
    (. + (strenv(PATHS) | split(" ") | map(select(. != "")) | map({"path": .})) | unique_by(.path))'
