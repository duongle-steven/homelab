#!/bin/sh
# The stage the cluster Secret carries (label homelab/stage, step 12b-2): platform leaves out whatever deploys an app,
# all deploys everything. A deploy that names none keeps the cluster's stage, and a new cluster gets all; it only moves
# forward, since lowering all to platform would delete every app's Application and their volumes.
# Usage: resolve-stage.sh <requested (empty: keep)> <the cluster's current label (empty: none)>
set -eu
requested=${1:-}
current=${2:-}
case "$requested" in
"" | platform | all) ;;
*) echo "resolve-stage: stage must be platform or all, not \"$requested\"" >&2; exit 1 ;;
esac
if [ "$requested" = platform ] && [ "$current" = all ]; then
	echo "resolve-stage: the stage only moves forward: this cluster runs all, so platform would delete its apps" >&2
	exit 1
fi
echo "${requested:-${current:-all}}"
