#!/bin/sh
# Reports the run to Forgejo as the commit status ci/pipeline (templates/pipeline.yaml: pending at the start, the
# Workflow's end from its exit handler). Inputs: STATE (pending or Argo's Succeeded, Failed, Error), DOMAIN, REPO, SHA,
# WORKFLOW, FORGEJO_TOKEN
set -eu
case "$STATE" in
pending) result=pending description="running" ;;
Succeeded) result=success description="passed" ;;
Failed) result=failure description="failed" ;;
*) result=error description="did not finish ($STATE)" ;;
esac
curl -fsS -o /dev/null -X POST "https://git.$DOMAIN/api/v1/repos/$REPO/statuses/$SHA" \
	-H "Authorization: token $FORGEJO_TOKEN" -H "Content-Type: application/json" \
	-d "{\"state\": \"$result\", \"context\": \"ci/pipeline\", \"description\": \"$description\",
		\"target_url\": \"https://ci.$DOMAIN/workflows/ci/$WORKFLOW\"}"
echo "status: $REPO@$SHA $result"
