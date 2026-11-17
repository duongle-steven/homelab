# ${{ values.name }}

${{ values.description }}

Made from Backstage's template "A repository in org ci". Every push builds the `Containerfile` with the in-cluster CI
(the commit status `ci/pipeline`); a push to `main` also pushes `registry.<domain>/${{ values.name }}:<commit>` to zot,
signed and attested. `.ci.yaml` can change the build's context, Containerfile and image name.
