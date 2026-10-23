#!/bin/sh
# Pass the off-site backup location's S3 endpoint (R2's, which holds the account id: never in git) to every component
# of the root ApplicationSet as global.backupS3Url, the value system/velero's location takes (rendered Argo CD
# manifests on stdin, stdout). No endpoint: unchanged (the sandbox's is in system/velero's values-dev.yaml).
set -eu
endpoint=${1:-}
if [ -z "$endpoint" ]; then
	exec cat
fi
ENDPOINT="$endpoint" yq '
  (select(.kind == "ApplicationSet" and .metadata.name == "root") | .spec.template.spec.sources[0].helm.valuesObject.global.backupS3Url) = strenv(ENDPOINT)'
