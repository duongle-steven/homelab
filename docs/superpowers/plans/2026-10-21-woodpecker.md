# Woodpecker for Other Repositories on Forgejo Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repositories in Forgejo org `ci` get CI from Woodpecker in the cluster, building images with Buildah into
zot, signed in only by members of `ci` (Kanidm `homelab-admins`, `homelab-editors`); the sandbox proves it end to end.

**Architecture:** `platform/woodpecker` (chart woodpecker 3.7.5, app 3.19.0) wired to read its Forgejo OAuth client
and agent secret from OpenBao; `lab infra forgejo sync` creates org `ci`, its team `developers` and the OAuth2
application `woodpecker`, and writes its credentials to `woodpecker/forgejo`; Forgejo's Kanidm login source maps
groups to `ci`'s teams. Step pods run unprivileged in user namespaces; Buildah builds with chroot isolation and vfs.

**Tech Stack:** Woodpecker 3.19 (Kubernetes backend), Forgejo 17 chart (`groupTeamMap`), Buildah 1.43, Go (`cli/`,
forgejo-sdk v3; `test/`, chromedp), Helm, yq.

**Spec:** `docs/superpowers/specs/2026-10-21-woodpecker-design.md`

## Global Constraints

- Org `ci` is separate from `ops`: `sync` keeps refusing any team grant on `ops/homelab`.
- Woodpecker: `WOODPECKER_OPEN: true`, `WOODPECKER_ORGS: ci`, `WOODPECKER_ADMIN: forgejo_admin`,
  `WOODPECKER_EXPERT_WEBHOOK_HOST: http://woodpecker-server.woodpecker` (not `WOODPECKER_WEBHOOK_HOST`).
- Agents: `WOODPECKER_BACKEND_K8S_USER_NAMESPACES: true`; no `WOODPECKER_PLUGINS_PRIVILEGED`; every
  `WOODPECKER_BACKEND_K8S_*_ALLOW_FROM_STEP` explicitly `false` (tolerations included, its default is `true`).
- Server reads `woodpecker-secret` (`WOODPECKER_FORGEJO_CLIENT`, `WOODPECKER_FORGEJO_SECRET`,
  `WOODPECKER_AGENT_SECRET`) and `woodpecker-environment`; agents read only `woodpecker-agent`
  (`WOODPECKER_AGENT_SECRET`). `createAgentSecret: false`, `mapAgentSecret: false`.
- No checksum annotation (spec decision 3); #31 stays deferred, its step becomes 11b.
- Pinned: chart 3.7.5, `quay.io/buildah/stable:v1.43.5` (the docs' and test's pipeline), `docker.io/library/busybox:1.37.0`
  (the test's base image). Re-check each is still the latest stable when starting a task.
- Never print a secret: client secret, agent secret, zot's `ci` password, Woodpecker tokens.
- Basic checks: `direnv exec . scripts/ci-check all`; commit, then `make dev` from the main checkout only.

## Review Focus

1. A Kanidm user outside `homelab-admins`/`homelab-editors` must not register in Woodpecker — Task 4 refuses
   `dev-viewer`.
2. A step pod must not be privileged, must run in a user namespace and must not use the agent's service account —
   Task 1 renders the agent env, Task 4 inspects a running step pod.
3. `sync` must keep the OAuth client and `woodpecker/forgejo` when they are right and must not touch `ops` —
   Task 3's unit tests and `TestForgejoDeploy`.
4. Removing someone from the Kanidm group must drop their `ci` membership at the next login
   (`groupTeamMapRemoval`) — Task 2 renders it; Task 4 checks `dev-editor` is in `ci/developers` after login.
5. Sandbox-only TLS skipping must not leak into production values — Task 1's render test checks production has no
   `SKIP_VERIFY`.

---

### Task 1: the chart, its sandbox values and its render test

**Files:**
- Modify: `platform/woodpecker/values.yaml`, `platform/woodpecker/templates/secret.yaml`
- Create: `platform/woodpecker/values-dev.yaml`, `test/woodpecker_test.go`
- Modify: `system/argocd/values-dev.yaml` (add `platform/woodpecker` after `platform/forgejo`),
  `test/scopes.yaml` (move `platform/woodpecker/**` from the Smoke-only group to a new group, step 4 below),
  `scripts/ci-check:7` (add `TestWoodpeckerRender`)
- Delete: `.woodpecker/` and its line in `test/scopes.yaml`'s `skip`

- [ ] **Step 1: Write the failing render test** (`test/woodpecker_test.go`)

```go
package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderWoodpecker(t *testing.T, env, expr string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", `scripts/render-chart platform/woodpecker $1 | yq -N "$0"`, expr, env)
	cmd.Dir = ".."
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

// docs/superpowers/specs/2026-10-21-woodpecker-design.md, decisions 1, 3 and 4
func TestWoodpeckerRender(t *testing.T) {
	server := `select(.kind == "StatefulSet" and .metadata.name == "woodpecker-server") | .spec.template.spec.containers[0]`
	agent := `select(.kind == "StatefulSet" and .metadata.name == "woodpecker-agent") | .spec.template.spec.containers[0]`
	env := func(e, container, name string) string {
		return renderWoodpecker(t, e, container+` | .env[] | select(.name == "`+name+`") | .value`)
	}
	for _, e := range []string{"", "dev"} {
		t.Run("env="+e, func(t *testing.T) {
			assert.Equal(t, "ci", env(e, server, "WOODPECKER_ORGS"))
			assert.Equal(t, "true", env(e, server, "WOODPECKER_FORGEJO"))
			assert.Equal(t, "http://woodpecker-server.woodpecker", env(e, server, "WOODPECKER_EXPERT_WEBHOOK_HOST"))
			assert.Empty(t, env(e, server, "WOODPECKER_WEBHOOK_HOST"), "Woodpecker 2's name")
			assert.Empty(t, env(e, server, "WOODPECKER_PLUGINS_PRIVILEGED"))
			assert.Equal(t, "woodpecker-environment,woodpecker-secret",
				renderWoodpecker(t, e, server+` | [.envFrom[].secretRef.name] | sort | join(",")`))
			assert.Equal(t, "woodpecker-agent", renderWoodpecker(t, e, agent+` | [.envFrom[].secretRef.name] | join(",")`))
			assert.Equal(t, "true", env(e, agent, "WOODPECKER_BACKEND_K8S_USER_NAMESPACES"))
			for _, allow := range []string{"SERVICE_ACCOUNT_NAME", "POD_NODE_SELECTOR", "POD_LABELS", "POD_ANNOTATIONS",
				"POD_AFFINITY", "RUNTIME_CLASS", "POD_TOLERATIONS"} {
				assert.Equal(t, "false", env(e, agent, "WOODPECKER_BACKEND_K8S_"+allow+"_ALLOW_FROM_STEP"), allow)
			}
			// The chart's own agent Secret is not rendered: the value comes from OpenBao
			assert.Empty(t, renderWoodpecker(t, e, `select(.kind == "Secret" and .metadata.name == "woodpecker-default-agent-secret") | .metadata.name`))
			assert.Equal(t, "woodpecker/generated/agent", renderWoodpecker(t, e,
				`select(.kind == "ExternalSecret" and .metadata.name == "woodpecker-agent") | .spec.data[0].remoteRef.key`))
		})
	}
	assert.Empty(t, env("", server, "WOODPECKER_FORGEJO_SKIP_VERIFY"), "production verifies Forgejo's certificate")
	assert.Equal(t, "1", renderWoodpecker(t, "dev", `select(.kind == "StatefulSet" and .metadata.name == "woodpecker-agent") | .spec.replicas`))
}
```

Check the chart's actual rendered names first (`scripts/render-chart platform/woodpecker | yq 'select(.kind == "StatefulSet") | .metadata.name'`, and the agent Secret's name with `createAgentSecret: true`) and adjust the selectors, not the assertions.

- [ ] **Step 2: Run it to see it fail**

Run: `direnv exec . sh -c 'cd test && go test -count=1 -run TestWoodpeckerRender ./...'`
Expected: FAIL (`WOODPECKER_ORGS` empty, `woodpecker-agent` missing).

- [ ] **Step 3: Values** (`platform/woodpecker/values.yaml`)

```yaml
woodpecker:
  agent:
    replicaCount: 2
    # The agent secret comes from OpenBao (woodpecker-agent), not the chart's Secret
    mapAgentSecret: false
    extraSecretNamesForEnvFrom:
      - woodpecker-agent
    resources:
      requests: {cpu: 10m, memory: 64Mi}
    env:
      WOODPECKER_BACKEND_K8S_STORAGE_RWX: false
      # Agents will spawn pods to run workflow steps using the
      # Kubernetes backend instead of running them directly on
      # the agent pod, so we can run many workflows per agent.
      WOODPECKER_MAX_WORKFLOWS: 10
      # Step pods: root inside is an unprivileged user on the node; pipelines cannot pick a service account, node,
      # labels, annotations, affinity, runtime class or tolerations (docs/superpowers/specs/2026-10-21-woodpecker-design.md)
      WOODPECKER_BACKEND_K8S_USER_NAMESPACES: true
      WOODPECKER_BACKEND_K8S_SERVICE_ACCOUNT_NAME_ALLOW_FROM_STEP: false
      WOODPECKER_BACKEND_K8S_POD_NODE_SELECTOR_ALLOW_FROM_STEP: false
      WOODPECKER_BACKEND_K8S_POD_LABELS_ALLOW_FROM_STEP: false
      WOODPECKER_BACKEND_K8S_POD_ANNOTATIONS_ALLOW_FROM_STEP: false
      WOODPECKER_BACKEND_K8S_POD_AFFINITY_ALLOW_FROM_STEP: false
      WOODPECKER_BACKEND_K8S_RUNTIME_CLASS_ALLOW_FROM_STEP: false
      WOODPECKER_BACKEND_K8S_POD_TOLERATIONS_ALLOW_FROM_STEP: false
  server:
    createAgentSecret: false
    resources:
      requests: {cpu: 50m, memory: 128Mi}
    persistentVolume:
      size: 2Gi
    env:
      WOODPECKER_EXPERT_WEBHOOK_HOST: http://woodpecker-server.woodpecker
      WOODPECKER_FORGEJO: true
      WOODPECKER_OPEN: true
      # Only members of Forgejo org ci register (Kanidm homelab-admins and homelab-editors, platform/forgejo)
      WOODPECKER_ORGS: ci
      WOODPECKER_ADMIN: forgejo_admin
    # WOODPECKER_HOST and WOODPECKER_FORGEJO_URL (templates/environment.yaml); the Forgejo client and the agent
    # secret from OpenBao (templates/secret.yaml)
    extraSecretNamesForEnvFrom:
      - woodpecker-environment
      - woodpecker-secret
# Host label: ci.<global.baseDomain>
host: ci
```

Check how the chart renders boolean env values (quoted strings): if `false` becomes `"false"` fine; if the chart drops `false`, quote them (`'false'`) and keep the test.

- [ ] **Step 4: The agent's ExternalSecret** (`platform/woodpecker/templates/secret.yaml`): keep `woodpecker-secret`
  as is and append, after `---`, an ExternalSecret `woodpecker-agent` with one item `WOODPECKER_AGENT_SECRET` from
  `woodpecker/generated/agent`, property `secret`.

- [ ] **Step 5: Sandbox values** (`platform/woodpecker/values-dev.yaml`)

```yaml
woodpecker:
  agent:
    # One agent is enough for the tests (the sandbox has about 4 GiB free)
    replicaCount: 1
  server:
    env:
      # The sandbox CA (trust-manager's ConfigMap homelab-dev-ca): Forgejo's OAuth and API calls
      SSL_CERT_DIR: /etc/ssl/certs:/etc/homelab-dev-ca
    extraVolumes:
      - name: homelab-dev-ca
        configMap:
          name: homelab-dev-ca
          optional: true
    extraVolumeMounts:
      - name: homelab-dev-ca
        mountPath: /etc/homelab-dev-ca
        readOnly: true
```

Step pods cannot mount the CA: the test's pipeline skips TLS verification itself (Task 4), nothing else does.
Match how `platform/forgejo/values-dev.yaml` and `platform/zot/values-dev.yaml` mount the CA if they differ.

- [ ] **Step 6: Sandbox component and scopes:** add `- path: platform/woodpecker` to the `root` generator in
  `system/argocd/values-dev.yaml`; in `test/scopes.yaml` remove `.woodpecker/**` from `skip` and
  `platform/woodpecker/**` from the Smoke-only group, add the group (Task 4's tests):

```yaml
  - paths: [platform/woodpecker/**]
    tests: Woodpecker
```

  add `Woodpecker` to `full` (before `NodeRestart`) and to the `platform/kanidm/**`, `platform/forgejo/**`,
  `platform/zot/**`, the secrets group and the `lab` Forgejo group's tests. Add `TestWoodpeckerRender` to
  `offline_tests` in `scripts/ci-check`. Delete `.woodpecker/`.

- [ ] **Step 7: Run the test and the checks**

Run: `direnv exec . sh -c 'cd test && go test -count=1 -run TestWoodpeckerRender ./...'` → PASS;
`direnv exec . scripts/ci-check all` → exit 0 (`check-mesh-policies`, `check-env-config` and `check-secret-stores`
pass: the new ExternalSecret reads its own namespace's branch).

- [ ] **Step 8: Commit** `feat(woodpecker): read the Forgejo client and agent secret from OpenBao; org ci only; unprivileged steps in user namespaces; in the sandbox`

---

### Task 2: Forgejo maps Kanidm groups to org `ci`'s teams

**Files:**
- Modify: `platform/forgejo/values.yaml` (the `Kanidm` oauth entry)
- Modify: `test/woodpecker_test.go` (a render assertion)

- [ ] **Step 1: Failing assertion** in a new `TestWoodpeckerForgejoTeams` (offline, add to `offline_tests`): render
  `platform/forgejo` and read the init container's login-source command (the chart renders `--group-team-map` into
  the `configure-gitea` script); assert it holds `{"homelab-admins":{"ci":["Owners"]},"homelab-editors":{"ci":["developers"]}}`
  (compare parsed JSON) and `--group-team-map-removal`.

- [ ] **Step 2: Values** — in the `Kanidm` entry, after `adminGroup`:

```yaml
        # Woodpecker's org ci (lab infra forgejo sync creates it and team developers): admins own it (org secrets),
        # editors run pipelines; leaving the Kanidm group drops the membership at the next login
        groupTeamMap: '{"homelab-admins": {"ci": ["Owners"]}, "homelab-editors": {"ci": ["developers"]}}'
        groupTeamMapRemoval: true
```

- [ ] **Step 3:** test PASS, `ci-check all` exit 0. The login source is registered at every start, so a running
  Forgejo picks it up at its next restart (Argo CD rolls it: the init script changes).

- [ ] **Step 4: Commit** `feat(forgejo): Kanidm admins own org ci, editors are its developers`

---

### Task 3: `lab infra forgejo sync` creates org `ci`, team `developers` and the Woodpecker OAuth client

**Files:**
- Modify: `cli/internal/forgejo/sync.go`, `cli/internal/forgejo/fake_test.go`, `cli/internal/forgejo/sync_test.go`,
  `cli/internal/cli/infra_forgejo.go` (pass the Woodpecker callback from `e`'s domain), the package comment
- Modify: `test/forgejo_test.go` (`TestForgejoDeploy`)

**Interfaces:**
- `const CIOrg = "ci"`, `CITeam = "developers"`, `WoodpeckerPath = "woodpecker/forgejo"`.
- `Sync(ctx, ep, adminPassword, tokens, woodpeckerCallback string)`: after `ops`, `ensureCIOrg(admin)` and
  `ensureWoodpeckerClient(admin, tokens, callback)`.

- [ ] **Step 1: Fake Forgejo** (`fake_test.go`): orgs `GET/POST /api/v1/orgs[/{org}]`, teams
  `GET /api/v1/orgs/{org}/teams`, `POST /api/v1/orgs/{org}/teams`, `PATCH /api/v1/teams/{id}`, OAuth2 apps
  `GET/POST /api/v1/user/applications/oauth2`, `PATCH /api/v1/user/applications/oauth2/{id}`, counting writes.

- [ ] **Step 2: Failing unit tests** (`sync_test.go`):
  - `TestSyncCreatesCIOrg`: a first sync creates private org `ci` and team `developers` (permission `write`, units
    `repo.code` and `repo.pulls` write, `can_create_org_repo: true`, `includes_all_repositories: false`); a second
    makes no write.
  - `TestSyncFixesCITeam`: a team edited by hand (`admin`, all repositories) is restored.
  - `TestSyncCreatesWoodpeckerClient`: creates a confidential app `woodpecker` with the redirect
    `https://ci.example.test/authorize`, writes `client_id`, `client_secret`, `redirect_uri` to `woodpecker/forgejo`;
    a second sync keeps both (no write to Forgejo or the store).
  - `TestSyncReplacesWoodpeckerClient`: a changed redirect or a store whose `client_id` differs → the app is updated
    and the new credentials written.
  - `TestSyncRefusesTwoWoodpeckerClients`: two apps named `woodpecker` → error naming them, nothing written.
  - Every error message checked not to contain the fake's secrets (as `TestStoreErrorsHideValues`).

- [ ] **Step 3: Run** `make -C cli test` → FAIL.

- [ ] **Step 4: Implement** in `sync.go`, ported from upstream `nixos`'s `syncDevelopers` and `syncOAuth`
  (`cli/internal/forgejo/forgejo.go` there) with our conventions: `ensureCIOrg` (org private, `notFound` checks, the
  team created or edited to the desired options, never added to `ops`), `ensureWoodpeckerClient` (paged
  `ListOauth2`, refuse duplicates, keep when `ConfidentialClient`, redirect URIs, stored `client_id`, a non-empty
  stored `client_secret` and `redirect_uri` all match; otherwise create or update and write all three keys; an
  update returns a new secret, so always write after one). Errors name the resource, never a value.

- [ ] **Step 5: CLI:** `infra_forgejo.go` passes `"https://ci." + e.Domain + "/authorize"` (the env's
  `global.baseDomain` from `infra/<env>/cluster/config.json`; use the field `forgejoEnv` already reads for the
  Forgejo URL). Update `sync`'s `Short`/`Long`: org `ci` and the Woodpecker client.

- [ ] **Step 6: `TestForgejoDeploy`:** after the first sync, `baoValue(t, "woodpecker/forgejo", "client_id")` and
  `client_secret` are non-empty, `redirect_uri` is `https://ci.<sandbox domain>/authorize`; after the second sync both
  are unchanged (compare in memory).

- [ ] **Step 7:** `make -C cli test check` PASS, `ci-check all` exit 0. Commit
  `feat(lab): forgejo sync creates org ci, its developers team and Woodpecker's OAuth client`

---

### Task 3b: break-glass deploy without GitHub (spec decision 10)

**Files:**
- Modify: `cli/internal/cli/infra_forgejo.go` (`push`'s `--break-glass` flag), its tests, `Makefile` (`break_glass`),
  `test/` `TestMakeInfraProduction`
- Modify: `docs/how-to-guides/deploy-to-forgejo.md` (section "Break-glass: GitHub or the internet is down")

- [ ] **Step 1: Failing tests:** `push --env sandbox --break-glass=x` → error "break-glass is for production";
  `--break-glass=""` given explicitly → error "a reason is required"; with a reason, `verifyProduction` is not
  called (inject the verifier as a function variable so the test sees it skipped) and HEAD is still required to be
  clean and to fast-forward Forgejo's `master` (`forgejo.Push` already refuses a non-fast-forward: assert its error
  passes through). `TestMakeInfraProduction`: `make -n infra env=production break_glass=outage` runs no `verify`
  and passes `--break-glass=outage` to `push`.
- [ ] **Step 2: Implement:** the flag (`cmd.Flags().Changed` to tell "given empty" from "absent"); when set, print
  `Break-glass deploy of %.12s: <reason>; bring this commit to GitHub's master through dev afterwards.` and skip
  `verifyProduction`; the `--commit` guard still applies. `Makefile`: `break_glass` skips the `verify` line and adds
  the flag to `push`; `force=1` and `break_glass` together are refused.
- [ ] **Step 3: Docs** (`deploy-to-forgejo.md`): when to use it (GitHub down; a fix that cannot wait), the commands
  (`scripts/ci-check all` locally first, commit on a branch, `make infra env=production break_glass="<reason>"`, or
  `lab infra forgejo push --break-glass=...` alone when R2 is unreachable), the follow-up (push the branch, PR into
  `dev` with the reason, release PR; merge commits only, never squash, so `master` gets this commit), the limits
  (charts on `*.github.io`, images on `ghcr.io`, no OpenTofu without R2, Argo CD's cache), the last resort (pause
  auto-sync, `kubectl`, resume).
- [ ] **Step 4:** `make -C cli test check`, `ci-check all`; commit `feat(lab): break-glass deploy when GitHub is down`

---

### Task 4: the sandbox test `TestWoodpecker`

**Files:**
- Create: `test/woodpecker_sandbox_test.go`
- Modify: `test/sso_test.go` if a shared host constant belongs there (`woodpeckerHost = "ci." + sandboxDomain`)

**Flow** (`skipUnlessSandbox`; one top-level test, subtests in order, `t.Cleanup` for everything created):

- [ ] **Step 1: Prerequisites:** `waitForApp` on Forgejo; `lab(t, "infra", "forgejo", "sync", "--env", "sandbox")`;
  wait up to 10 minutes for `https://ci.<domain>/healthz` to answer 200 (the server starts once ESO writes
  `woodpecker-secret`).

- [ ] **Step 2: Sign-in:** `appLogin` for `dev-editor` at `https://ci.<domain>/authorize` (Woodpecker → Forgejo's
  OAuth → Forgejo's Kanidm login → Forgejo's grant page → back; `acceptConsent` must also accept Forgejo's grant page:
  extend it if needed). Assert the Woodpecker session works (`GET /api/user` with the cookies: login `dev-editor`).
  `dev-viewer`: the same flow ends on Woodpecker's error page (not registered; assert the message Woodpecker 3.19
  shows for an org it does not allow, captured once by hand) and `GET /api/user` is 401. Through Forgejo's API
  (admin): `dev-editor` is a member of `ci/developers`, `dev-admin` (after its own sign-in) of `ci/Owners`.

- [ ] **Step 3: Tokens:** with each session, create a Woodpecker personal token (`POST /api/user/token` with the
  session's CSRF header from `/web-config.js`; check the API) for `dev-editor` and `dev-admin`. A Forgejo token for
  `dev-editor` through the admin API (`sudo` or by resetting nothing: create it with Basic auth of the admin
  impersonating via `Sudo` header) scoped `write:repository`.

- [ ] **Step 4: Org secret:** as `dev-admin`, look up org `ci` (`GET /api/orgs/lookup/ci`) and create secrets
  `registry_username` = `ci` and `registry_password` = `baoValue(t, "zot/generated/ci", "password")`, events `push`
  (the docs' command does the same with woodpecker-cli).

- [ ] **Step 5: The test repository:** as `dev-editor`, create `ci/woodpecker-probe-<unix>` (auto-init), commit
  `Dockerfile` (`FROM docker.io/library/busybox:1.37.0` + `RUN echo probe > /probe.txt`) and `.woodpecker.yaml`:

```yaml
when:
  - event: push
skip_clone: false
clone:
  - name: clone
    image: docker.io/woodpeckerci/plugin-git:<latest, pinned>
    settings:
      # The sandbox's CA cannot reach step pods; production verifies (docs/how-to-guides/woodpecker.md)
      skip_verify: true
steps:
  - name: image
    image: quay.io/buildah/stable:v1.43.5
    environment:
      REGISTRY_USERNAME: {from_secret: registry_username}
      REGISTRY_PASSWORD: {from_secret: registry_password}
    commands:
      - buildah --storage-driver vfs build --isolation chroot -t registry.<domain>/${CI_REPO_NAME}:${CI_COMMIT_SHA} .
      - echo "$REGISTRY_PASSWORD" | buildah login --tls-verify=false -u "$REGISTRY_USERNAME" --password-stdin registry.<domain>
      - buildah --storage-driver vfs push --tls-verify=false registry.<domain>/${CI_REPO_NAME}:${CI_COMMIT_SHA}
```

  Activate it as `dev-editor` (`POST /api/repos?forge_remote_id=<Forgejo repo id>`), then push a commit (Forgejo API
  file update) to trigger the webhook.

- [ ] **Step 6: The pipeline:** poll `GET /api/repos/{id}/pipelines` up to 10 minutes for the push's pipeline;
  while its step runs, read the step pod (`kubectl get pods -n woodpecker` with the label Woodpecker sets, check
  it): `hostUsers: false`, no container `privileged`, `serviceAccountName` not the agent's. Assert the pipeline's
  status `success`; on failure print its step logs (`GET /api/repos/{id}/logs/{number}/{step}`; they hold no
  secret: Woodpecker masks `from_secret` values).

- [ ] **Step 7: zot serves the tag:** `GET https://registry.<domain>/v2/<repo>/manifests/<sha>` as `ci` → 200.

- [ ] **Step 8: Cleanup** (`t.Cleanup`, best effort, logged): Woodpecker repo deleted, org secrets deleted, Forgejo
  repository deleted, the image deleted from zot as `dev-admin`'s API key or skipped with a log (zot's `ci` cannot
  delete; a probe tag left in the sandbox is harmless; never in production since the test is sandbox-only).

- [ ] **Step 9:** `go vet`, `ci-check all`, commit `test(woodpecker): sign-in by org, a Buildah pipeline into zot, unprivileged steps`

---

### Task 5: docs, CLAUDE.md, deferred minors

**Files:**
- Create: `docs/how-to-guides/woodpecker.md` (and its `mkdocs.yml` nav entry)
- Modify: `docs/concepts/secrets-management.md`, `docs/how-to-guides/deploy-to-forgejo.md`,
  `docs/reference/architecture/overview.md` if it lists Woodpecker's wiring, `CLAUDE.md`,
  `docs/superpowers/deferred-minors.md`, `docs/reference/roadmap.md` if it mirrors the roadmap

- [ ] **Step 1: `woodpecker.md`:** who signs in (Kanidm `homelab-admins` own `ci`, `homelab-editors` are its
  developers; others are refused; a removal takes effect at the next Forgejo login); bringing a repository (create it
  in `ci`, or a pull mirror); activating it; the org secret (a `homelab-admins` member, woodpecker-cli or the UI, the
  value piped from `bao kv get -field=password secret/zot/generated/ci` in OpenBao's pod with the admin role, never
  echoed); a `.woodpecker.yaml` with Buildah (as Task 4, without `skip_verify`/`--tls-verify=false`); what steps may
  not do (privilege, service accounts, node selection) and why; after `sync` replaced the OAuth client, `kubectl
  rollout restart statefulset/woodpecker-server -n woodpecker`.
- [ ] **Step 2:** `secrets-management.md`: `woodpecker/forgejo` written by `sync`; `deploy-to-forgejo.md`: `sync`
  also creates org `ci`, team `developers`, the OAuth client.
- [ ] **Step 3: CLAUDE.md:** 10e done (one sentence of what it is, in the roadmap's style); Sandbox section: Woodpecker
  runs in the sandbox, `make dev`'s group list gains `Woodpecker`; production checklist: replace "Woodpecker's server
  does not read `woodpecker-secret` yet" and "Woodpecker reads `secret/woodpecker/forgejo` … which nothing writes yet"
  with what is still unproven (Woodpecker on rook-ceph PVCs, two agents, TLS from step pods to `git.`/`registry.`,
  the blog's repository); egress table line unchanged.
- [ ] **Step 4: `deferred-minors.md`:** remove "dev shell cleanup"; #31's step → 11b (Reloader or explicit restarts).
- [ ] **Step 5:** `ci-check all` (mkdocs strict) exit 0; commit `docs: Woodpecker for org ci (10e)`

---

### Task 6: proof on the sandbox

- [ ] **Step 1:** `make dev` from the main checkout (scope from the branch: Woodpecker, Forgejo, Identity, SSO, …);
  every test passes; record counts and times.
- [ ] **Step 2:** resources at rest after the run (`kubectl top pods -n woodpecker`, the node's memory) for the PR body.
- [ ] **Step 3:** `make dev scope=full` once before the PR (the scope change touches `full`).
