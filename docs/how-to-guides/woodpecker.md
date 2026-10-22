# Woodpecker

Woodpecker (`platform/woodpecker`, `https://ci.<domain>`) runs CI for repositories in Forgejo's org `ci`: a push runs
the repository's `.woodpecker.yaml`, each step in its own pod in namespace `woodpecker`. This repository is not one
of them: its CI is GitHub Actions, and `ops/homelab` only receives deploys ([Deploy to Forgejo](deploy-to-forgejo.md)).

## Who signs in

Woodpecker signs people in through Forgejo (OAuth2 application `woodpecker`, created by `lab infra forgejo sync`), and
Forgejo through Kanidm. Only members of org `ci` may register (`WOODPECKER_ORGS`); Forgejo's Kanidm login source sets
that membership from Kanidm's groups at every Forgejo login:

| Kanidm group      | In org `ci`       | In Woodpecker                                           |
|-------------------|-------------------|---------------------------------------------------------|
| `homelab-admins`  | team `Owners`     | runs pipelines, manages the org's secrets               |
| `homelab-editors` | team `developers` | runs pipelines of the repositories they create or join |
| anyone else       | none              | refused                                                 |

Leaving the Kanidm group takes effect at the person's next Forgejo login; until then their Woodpecker session lasts.

## Bring a repository

1. In Forgejo, create it in org `ci` (or New migration → a pull mirror of a GitHub repository, owner `ci`).
2. In Woodpecker, Add repository → enable it. Woodpecker adds its webhook to Forgejo (to the in-cluster Service).
3. Push a `.woodpecker.yaml` (below).

## Build an image into zot

Steps run unprivileged: no privileged plugins, root inside a step is an unprivileged user on the node (a user
namespace), and a pipeline cannot choose a service account, node, labels, annotations, affinity, runtime class or
tolerations. Build images with Buildah (no daemon, no privilege), pushed to zot as its machine account `ci`:

```yaml
when:
  - event: push
    branch: ${CI_REPO_DEFAULT_BRANCH}
steps:
  - name: image
    image: quay.io/buildah/stable:v1.43.5
    environment:
      REGISTRY_USERNAME: {from_secret: registry_username}
      REGISTRY_PASSWORD: {from_secret: registry_password}
    commands:
      - image=registry.<domain>/${CI_REPO_NAME}:${CI_COMMIT_SHA}
      - buildah --storage-driver vfs build --isolation chroot -t "$image" .
      - echo "$REGISTRY_PASSWORD" | buildah login -u "$REGISTRY_USERNAME" --password-stdin registry.<domain>
      - buildah --storage-driver vfs push "$image"
```

`vfs` is slower than overlay and each step starts from scratch, so there is no layer cache between runs.

## The registry secrets (once per org, a `homelab-admins` member)

`registry_username` is `ci`; `registry_password` is zot's `ci` password from OpenBao, piped into Woodpecker's API so
it never shows. Woodpecker knows org `ci` only once one of its repositories is active, so activate one first. Create
a personal token in Woodpecker (your avatar → Personal access token), then, naming that repository:

```sh
read -rs WOODPECKER_TOKEN && export WOODPECKER_TOKEN    # paste it; nothing echoes
ci=https://ci.<domain>
org=$(curl -fsS -H "Authorization: Bearer $WOODPECKER_TOKEN" "$ci/api/repos/lookup/ci/<repository>" | jq .org_id)
secret() {   # name, value on stdin
  jq -Rc --arg name "$1" '{name: $name, value: ., events: ["push", "tag", "manual"]}' |
    curl -fsS -o /dev/null -H "Authorization: Bearer $WOODPECKER_TOKEN" -H 'Content-Type: application/json' \
      -d @- "$ci/api/orgs/$org/secrets"
}
echo ci | secret registry_username
kubectl -n openbao create token openbao-admin --audience=openbao --duration=10m |
  kubectl -n openbao exec -i openbao-0 -c openbao -- \
    sh -c 'export BAO_TOKEN=$(bao write -field=token auth/kubernetes/login role=admin jwt=-); bao kv get -field=password secret/zot/generated/ci; bao token revoke -self >/dev/null' |
  secret registry_password
```

After zot's `ci` password is rotated, update `registry_password` the same way (`PATCH
$ci/api/orgs/$org/secrets/registry_password`).

## When something changes

- `lab infra forgejo sync` replaced the OAuth client (someone changed it in Forgejo): External Secrets refreshes
  `woodpecker-secret` within the hour (or `kubectl -n woodpecker annotate externalsecret woodpecker-secret
  force-sync=$(date +%s) --overwrite`), then `kubectl rollout restart statefulset/woodpecker-server -n woodpecker`.
- Woodpecker keeps its data (users, repositories, secrets, logs) in SQLite on the server's PVC.
