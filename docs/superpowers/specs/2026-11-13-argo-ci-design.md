# Step 10f: Argo Workflows and Argo Events replace Woodpecker

Status: design agreed in chat 2026-11-13, written spec awaiting review. Branch: `feat/argo-ci` (PR into `dev`).

## Intent

The homelab's in-cluster CI, for repositories in Forgejo's org `ci`, moves from Woodpecker (step 10e) to Argo Workflows
driven by Argo Events. Woodpecker never ran a real pipeline (only the tests' probe), so nothing migrates: it is removed
before the first production deploy, which then never carries it.

Why Argo (chosen by the user, 2026-11-13, after comparing Tekton, Forgejo Actions and Woodpecker):

- Forgejo Actions would teach GitHub Actions' syntax, which this repository's own CI already uses every day, and its
  runner still needs Docker or Podman: on Kubernetes a privileged Docker-in-Docker pod, against Pod Security and
  step 15h. Its Kubernetes backends are community experiments.
- Tekton (Pipelines-as-Code, Chains) has more of a CI built in: pipelines in the repository, status on pull requests,
  signing and provenance by a controller. Argo Workflows has none of the three and this step builds them, but it is a
  CNCF graduated project with a wider community, a general workflow engine (batch, the teaching app's sessions later,
  step 18b), SSO with groups built in, one family with Argo CD and Argo Rollouts, and part of CAPA, which the user's
  Golden Kubestronaut goal counts. Revisit Tekton if it graduates and joins that list.
- The homelab still picks what is best for itself; certifications only break ties. Exam-only setups (kubeadm, Cilium)
  go to the teaching app's extension, not here.

GitHub Actions stays this repository's CI. Argo serves repositories hosted on Forgejo (the blog, Backstage's app,
later ones); a public repository that wants outside contributions is better served by GitHub Actions (forks, keyless
signing, free runners), decided per repository.

This step comes first in the order agreed 2026-11-13, before production: CI (this step), then OpenTelemetry, then
Backstage (built by this CI, its templates creating repositories with this CI's golden path), then the blog with Argo
Rollouts (its repository from Backstage's template, its image from this CI, canary through Istio).

## Goals

- A push to a repository in org `ci` builds its image with Buildah, unprivileged; the default branch pushes it to zot
  as `registry.<domain>/<repo>:<commit sha>`, signs it and attests its provenance; Forgejo shows the result as a commit
  status (pull requests show it too).
- Pipelines are a golden path: templates in this repository (`WorkflowTemplate`s, reviewed and rendered like any
  chart), never pipelines written in the built repositories. A repository chooses only parameters.
- Nothing a `ci` member pushes can run arbitrary pod specs: the controller accepts only Workflows that reference the
  templates (`workflowRestrictions.templateReferencing: Strict`).
- People sign in to Argo's UI with Kanidm (SSO, groups), as with every admin tool.
- Everything Woodpecker held in the repository goes: chart, Forgejo OAuth client, rotation entries and kinds, backup
  group, egress row, Falco and Kyverno exceptions, scan acceptances, homepage tile, tests, docs.
- In the sandbox too, proven by e2e specs.

## Non-goals

- This repository's CI (stays GitHub Actions), `ops/homelab` (only receives deploys).
- The blog's and Backstage's repositories and pipelines (their own steps); a pipeline per language (tests, linters):
  the golden path builds images only, more templates come with the repositories that need them.
- Pull requests from forks or strangers: only `ci` members' repositories run.
- An artifact repository and archived logs: pod logs already reach Loki (Alloy); the UI's logs last as long as the
  pods.
- Argo Rollouts (its own step), Argo CD Image Updater (Renovate or a commit bumps image tags).
- Enforcing image signatures (step 15b-2 switches Kyverno to Enforce).

## Anti-goals

- A privileged pod, a Docker daemon, or a step choosing its service account, node or host users.
- Webhook payloads trusted without authentication.
- A signing key readable by a workflow pod (it signs through OpenBao, below).
- A second CI kept "for now" beside Woodpecker's removal.

## Constraints

- Latest stable releases, pinned (2026-11-13: chart `argo-workflows` 2.0.18 with Argo Workflows v4.1.6, chart
  `argo-events` 2.4.28 with Argo Events v1.9.11, from `https://argoproj.github.io/argo-helm`). v4.0.0 to v4.0.4 had an
  SSO RBAC crash (CVE-2026-42183); 4.1.6 is past it.
- Every rule in CLAUDE.md applies: securityContext or a `PolicyException` with its reason, `homelab/backup` on every
  PVC, ambient mesh with an `allow` per namespace, egress denied unless listed, secrets in OpenBao through ESO, every
  secret in the rotation catalog, settings from `config.json` (hosts as `<host>.{{ .Values.global.baseDomain }}`),
  `test/scopes.yaml` entries for new components.
- Step 15h's Buildah seccomp profile (`homelab/buildah.json`) and its native admission policies carry over to the
  namespace where builds run; the profile itself does not change.

## Design

### Components and namespaces

| Path | Namespace | Holds |
|------|-----------|-------|
| `platform/argo-workflows` | `argo-workflows` | the controller and Argo Server (UI, `https://ci.<domain>`, LAN Gateway), Kanidm client `argo-workflows` |
| `platform/argo-events` | `argo-events` | Argo Events' controller (no validating webhook) |
| `platform/ci` | `ci` | the EventBus, the Forgejo EventSource and Sensor, the `WorkflowTemplate`s, the Workflows and their pods, their ExternalSecrets, the seccomp admission policies |

Workflows run only in `ci` (the controller's namespace list or `--managed-namespace`, builder's call): the controller
and server stay where no pipeline runs.

### From a push to a Workflow

1. `lab infra forgejo sync` keeps an org webhook on `ci` (push and pull request events, JSON) pointing at the
   EventSource's Service in namespace `ci`, sending `Authorization: Bearer <secret>`; the secret is generated in
   OpenBao (`ci/generated/webhook`, `platform/generated-secrets`), and sync reads it there. Forgejo's
   `webhook.ALLOWED_HOST_LIST: private` already admits the in-cluster address.
2. A webhook EventSource checks the bearer secret (`authSecret`); Argo Events has no Forgejo or Gitea source, so the
   Sensor reads Forgejo's JSON itself.
3. The EventBus is JetStream (its minimum replica count; check whether the sandbox can run fewer).
4. The Sensor filters push to any branch and pull request opened/synchronized, and creates a Workflow from
   `workflowTemplateRef: pipeline` with the repository, commit, ref and event as parameters, through its own service
   account (create Workflows in `ci`, nothing else).

### The golden path (`platform/ci/templates/`)

`WorkflowTemplate` `pipeline` (the only entry point the Sensor uses), built from smaller templates others can reuse
later:

- clone: Forgejo over the in-cluster address with the `ci` bot's token (read), at the commit;
- settings: optional `.ci.yaml` at the repository's root with only known keys (`context`, `containerfile`, `image`),
  defaults `.`, `Containerfile` or else `Dockerfile`, the repository's name; anything else fails the run; a
  repository with neither file and no `.ci.yaml` ends as success with nothing built;
- build: Buildah (`quay.io/buildah/stable`, pinned, the version step 15h measured or a newer one re-measured), chroot
  isolation, vfs, in a user namespace (`hostUsers: false` in the template's pod spec), under `homelab/buildah.json`;
  pull requests build only; the default branch pushes `registry.<domain>/<repo>:<sha>` to zot as its machine account
  `ci` (the existing htpasswd user, now a Secret in namespace `ci`);
- sign and attest (default branch only): cosign signs the pushed digest and attests an SLSA provenance predicate the
  pipeline writes (builder: Argo Workflows in this cluster; the workflow's name, template, parameters; materials: the
  repository at the commit, the base images' digests Buildah reports), both with an OpenBao transit key through
  cosign's `hashivault://` KMS, authenticated by the workflow pod's service account token (OpenBao role `ci-signer`,
  audience `openbao`, policy: sign with that key only). The key never leaves OpenBao; keyless signing is out because
  public Fulcio does not trust this cluster's tokens;
- exit handler: a commit status (`pending` at the start, then `success`, `failure` or `error`, linking the run in Argo's
  UI) through Forgejo's API with the `ci` bot's token.

Workflows carry `ttlStrategy` (kept 7 days after completion) and `podGC` (pods deleted on success; failed pods kept
with their Workflow for debugging); the builder picks the exact values.

### Who does what

- `lab infra forgejo sync`: drops Woodpecker's OAuth2 application (deleting it if present) and gains the org webhook
  and a bot user `ci-bot` (member of `ci`'s team `developers`: write, for statuses and clones; its token in OpenBao
  `ci/forgejo-token`, refused if it signed in with Kanidm, like `deploy`/`argocd`; a local user, so the Kanidm
  `groupTeamMapRemoval` never touches its membership). The Kanidm `groupTeamMap` stays.
- Argo Server: `--auth-mode=sso`, Kanidm OIDC client `argo-workflows` (confidential; `homelab_groups` from `claimMap`
  like the other admin tools; PKCE if Argo Server sends it, otherwise the client allows its absence: check), SSO RBAC
  by service accounts with `workflows.argoproj.io/rbac-rule`: `homelab-admins` full on `ci`'s Workflows,
  `homelab-editors` submit from the templates, resubmit, view and logs, `homelab-users` view only (the three groups
  every admin tool matches); nobody else. Argo Server keeps no session of its own beyond its cookie.
- The controller: `workflowRestrictions.templateReferencing: Strict`, so a Workflow submitted through the UI or API
  must reference `ci`'s templates.

### Seccomp, Pod Security, mesh, egress

- 15h's `MutatingAdmissionPolicy` and `ValidatingAdmissionPolicy` move to `platform/ci`, bound to namespace `ci`. Argo
  adds its own containers to every pod (the `init` and `wait` containers, argoexec), so "every container is Buildah"
  no longer identifies a build: the mutation matches pods of the build template (a label only templates in this
  repository set, which `templateReferencing: Strict` keeps out of anyone else's reach) whose main container is
  `quay.io/buildah/stable` with `hostUsers: false`; the validating policy still refuses a `Localhost` profile to every
  other pod in `ci`. argoexec under `homelab/buildah.json` (RuntimeDefault plus four syscalls) is expected to work;
  the e2e spec proves it.
- Pod Security `baseline` on `ci`, `restricted` where possible elsewhere; exceptions with reasons (Buildah's root in a
  user namespace, as Woodpecker's).
- Mesh: `argo-workflows`, `argo-events` and `ci` join it; `allow`s: the Gateway to Argo Server, Forgejo to the
  EventSource, Prometheus to the metrics ports; `ci`'s pods call Forgejo, zot, OpenBao and the API server
  (`EXTRA` in `scripts/check-mesh-policies` where renders cannot show it).
- Egress: `ci` gets 80/443 (base images, a build's downloads), as `woodpecker` had; the controllers need none.
- Falco: the Buildah memfd exception moves to namespace `ci`.
- Trust: in the sandbox the templates trust the local CA through the ConfigMap `homelab-dev-ca` (trust-manager copies
  it into `ci`; `values-dev.yaml`), never by skipping TLS verification as Woodpecker's sandbox pipeline did. In
  production, `ci`'s pods reach `git.` and `registry.<domain>` through public DNS: that they land on the LAN Gateway is
  checked before relying on it, as for the rotation CronJob (CLAUDE.md's production list carries it).

### Secrets and the rotation catalog

| Secret | Where | Rotation |
|--------|-------|----------|
| the webhook's bearer secret | `ci/generated/webhook` | generated, class 1: new value, `sync` updates Forgejo's webhook, the EventSource restarts |
| `ci-bot`'s Forgejo token | `ci/forgejo-token` | Forgejo token with overlap (the existing method) |
| zot's `ci` password | its existing path | zot's htpasswd and the Secret in `ci` change together; waits for no running Workflow in `ci` |
| Kanidm client `argo-workflows` | kaniop Secret | the existing kanidm-client kind, class 2 |
| the signing key | OpenBao transit `ci-signing` | manual, with the reason: a new key version needs Kyverno's public key updated first |

The kinds `woodpecker-agent` and `woodpecker-token`, the entries `woodpecker.agent`, `woodpecker.admin-token` and
`forgejo.woodpecker-oauth`, and `idle: woodpecker` go; class 2's "wait for no running pipeline" reads `ci`'s running
Workflows instead. OpenBao's initialize stanzas gain the transit engine, the key and the role `ci-signer`
(production has not started yet; a sandbox built before this step needs `just dev-fresh` once).

### Verification of images

A Kyverno `verifyImages` policy (Audit, like `verify-lab-image`) checks `registry.<domain>/*` against the transit key's
public key (published in the chart, exported once from OpenBao; the builder chooses how it reaches the policy without a
manual copy if one exists, otherwise a documented step) and the provenance attestation's predicate type.

### Backups

Argo keeps no volume (Workflows live in etcd for 7 days); JetStream's storage, if it has one, is `homelab/backup: none`
(an event lost in a restart is a push to repeat). Woodpecker's `daily` group entry goes.

## Testing

Offline (`test/offline`): renders of the three charts (restrictions on, SSO on, no Woodpecker anywhere), the seccomp
policies against fixtures of an Argo build pod and of other `ci` pods (replacing `TestWoodpeckerSeccompPolicies`), the
catalog and generated-secrets wiring, `scripts/check-*` updated.

E2e (`test/e2e`, labels from `test/scopes.yaml`, sandbox and testbed where they apply):

- `CI`: a fixture repository pushed to `ci` (a two-line Containerfile) builds, pushes to zot, is signed and attested
  (cosign verifies both with the public key), and Forgejo shows a `success` status; a pull request builds without
  pushing; a repository whose `.ci.yaml` has an unknown key fails with a `failure` status; a webhook without the bearer
  secret creates no Workflow.
- `CISeccomp`: the build pod runs under `homelab/buildah.json`; another `ci` pod naming a Localhost profile is refused;
  RuntimeDefault refuses `unshare` (the 15h specs, moved).
- `CIRestrictions`: a Workflow without a template reference is refused.
- `SSOArgoWorkflows`: admin, editor, viewer, outsider (like `SSOKiali`).
- `TestRotation` covers the new entries where they are scheduled.

## Documentation

`docs/how-to-guides/ci.md` replaces `woodpecker.md` (bring a repository, `.ci.yaml`, reading a run, who signs in);
architecture overview, secrets rotation, supply chain, runtime detection, cluster scan and backup guides lose
Woodpecker; CLAUDE.md's roadmap gains 10f (done when merged) and the agreed next steps (OpenTelemetry, Backstage, blog
with Rollouts), and its "before the first production deploy" list trades Woodpecker's unproven points for Argo's.

## Left to the builder

The exact controller and server values, TTL and podGC numbers, JetStream sizing, how the public key reaches Kyverno,
the label the seccomp mutation matches, the provenance predicate's fields, and whether Argo Server's UI host stays
`ci.<domain>` (the default here) — each recorded in the plan.
