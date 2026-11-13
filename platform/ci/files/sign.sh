#!/bin/sh
# Signs the pushed image and attests its SBOM and an SLSA provenance with OpenBao's transit key ci-signing, which never
# leaves OpenBao (templates/pipeline.yaml's sign step; cosign's hashivault KMS reads VAULT_ADDR and VAULT_TOKEN). No
# public transparency log: the registry is the homelab's. Inputs: SIGNED_REF (name@digest), DOMAIN, REPO, SHA, GIT_REF,
# CONTEXT, CONTAINERFILE, WORKFLOW, WORKFLOW_UID, STARTED, REGISTRY_USERNAME, REGISTRY_PASSWORD; /bao/token, /work/sbom.spdx.json
set -eu
VAULT_TOKEN=$(cat /bao/token)
export VAULT_TOKEN
printf '%s' "$REGISTRY_PASSWORD" | cosign login "registry.$DOMAIN" -u "$REGISTRY_USERNAME" --password-stdin
cosign sign --yes --use-signing-config=false --tlog-upload=false --key hashivault://ci-signing "$SIGNED_REF"
cat >/tmp/provenance.json <<JSON
{
  "buildDefinition": {
    "buildType": "https://ci.$DOMAIN/pipeline@v1",
    "externalParameters": {"repository": "https://git.$DOMAIN/$REPO", "ref": "$GIT_REF", "context": "$CONTEXT",
      "containerfile": "$CONTAINERFILE"},
    "resolvedDependencies": [{"uri": "git+https://git.$DOMAIN/$REPO@$GIT_REF", "digest": {"gitCommit": "$SHA"}}]
  },
  "runDetails": {
    "builder": {"id": "https://ci.$DOMAIN"},
    "metadata": {"invocationId": "$WORKFLOW_UID", "startedOn": "$STARTED"},
    "byproducts": [{"name": "workflow", "uri": "https://ci.$DOMAIN/workflows/ci/$WORKFLOW"}]
  }
}
JSON
cosign attest --yes --use-signing-config=false --tlog-upload=false --key hashivault://ci-signing --type slsaprovenance1 --predicate /tmp/provenance.json "$SIGNED_REF"
cosign attest --yes --use-signing-config=false --tlog-upload=false --key hashivault://ci-signing --type spdxjson --predicate /work/sbom.spdx.json "$SIGNED_REF"
echo "sign: $SIGNED_REF signed and attested (provenance, SPDX)"
