# Renovate

Renovate runs in the production cluster (`platform/renovate`, daily at 05:00) and opens pull requests into `dev` on
GitHub as the GitHub App `duongle-lab-renovate`. Its pull requests run `checks` and, labelled `sandbox`, the sandbox.
The App's private key goes through OpenTofu into OpenBao and External Secrets into the Secret `renovate-github-app`.
Each run mints its own installation token: the CronJob's initContainer `github-token` (the only container that mounts
the key; `platform/renovate/files/github-token.js`) signs a JWT and asks GitHub for a token with Renovate's
permissions, the Renovate container reads it from memory and revokes it when the run ends, failed or not. Passing
trouble (the network, GitHub's 5xx or 429) is retried three times; a refused token (a wrong key, App ID or permission)
fails the Job at once with GitHub's status and message (`kubectl -n renovate logs job/<job> -c github-token`). A
failed run is retried once, a run stops after 55 minutes (its token lives an hour), and a failed Job reaches ntfy
(`KubeJobFailed`). The pod runs as the image's user (uid 12021, gid 0) without privileges or a service account token.

## Create the App (once)

1. GitHub → Settings → Developer settings → GitHub Apps → New GitHub App: a name of your own (App names are global;
   this repository's is `duongle-lab-renovate`), homepage the repository's URL, **Webhook: off** (Renovate runs as a
   CronJob and polls; nothing listens for events).
2. Repository permissions: Contents, Pull requests, Workflows, Issues (Renovate's Dependency Dashboard): **Read and
   write**; Commit statuses, Checks: **Read-only**; Metadata: Read-only (always). No organization or account
   permissions. "Only on this account".
3. Create it, note the **App ID**, then **Generate a private key** (a `.pem` downloads once).
4. Install App → your account → **Only select repositories** → `homelab`. The installation's URL ends with its
   **installation ID** (`.../settings/installations/<id>`).
5. The bot's user id, for Renovate's commit author: `gh api '/users/<app-name>[bot]' --jq .id`.

App ID, installation ID and bot id are not secrets: they live in `platform/renovate/values.yaml`.

## Give the key to production (once, and after a rotation)

```sh
HOMELAB_GITHUB_APP_KEY=~/Downloads/<app-name>.<date>.private-key.pem make infra env=production
```

The path may come before `make` or after it, relative (to where `make` runs), absolute or `~/`, spaces included. OpenTofu keeps the key in its encrypted state (`infra/production/secrets`);
later runs need no file, so delete it.
If the unit was applied before the key existed, apply once with the key and
`-replace=module.github_app_key.terraform_data.kept` in `infra/production/secrets`
(`terragrunt apply -replace=...`); the plain run refuses rather than ignoring the key.

## Rotate the key

Generate a new key on the App's page, then in `infra/production/secrets`:

```sh
HOMELAB_GITHUB_APP_KEY=<new.pem> terragrunt apply -replace=module.github_app_key.terraform_data.kept
```

Delete the old key on GitHub once Renovate ran with the new one.

## What it updates

`renovate.json5` adds to Renovate's defaults (charts, `values*.yaml` images, Go, OpenTofu providers, GitHub Actions):
the images in `apps/*.yaml`, the app-template pin in `system/argocd/values*.yaml` (a regex manager) and `flake.lock`
(the nix manager, every Monday's lock file maintenance; schedules use the time zone in the global config), and the
sandbox's k3s image and Gateway API CRDs (`infra/_modules/k3d`, regex managers). `TestRenovateManagers` checks what
Renovate finds, offline. A new commit from Renovate on its labelled pull request (a rebase, a newer version) runs
`sandbox-label` again.

## Add a repository

Install the App on it (App → Configure → Repository access) and add a `renovate.json5`; the next run finds it
(`autodiscover`, limited to `duongle-steven/*`). Renovate skips repositories without a config.
