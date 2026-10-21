# Step 8e: generated secrets straight to OpenBao

Status: design approved in chat 2026-10-21, written spec awaiting review. Branch: `feat/generated-secrets` from `dev`.

## Goal

A generated secret has one value, the one in OpenBao, and nothing else holds a copy that can differ from it. Tests,
docs and people read generated values where the apps read them.

## Context

- `platform/global-secrets` (namespace `global-secrets`, from upstream `master`, where other namespaces read its
  Secrets): for each entry of `values.yaml`, a `Password` generator, an ExternalSecret (`refreshPolicy:
  CreatedOnce`) writing it into a Secret `<name>`, and a PushSecret (`updatePolicy: IfNotExists`, `refreshInterval:
  1h`, `deletionPolicy: None`, SecretStore `openbao-writer`, role `writer`) copying that Secret to each destination
  `secret/<namespace>/generated/<name>`. Apps read only OpenBao, through their namespace's `SecretStore` `openbao`.
- ESO 2.12's `CreatedOnce` (`shouldRefresh`) generates again when the ExternalSecret's status has no
  `SyncedResourceVersion` or the Secret looks invalid (missing from the cache, or a wrong label or data hash). A
  reconcile that reads a stale ExternalSecret right after the first sync regenerates the Secret. The PushSecret then
  keeps OpenBao's first value (`IfNotExists`), so the Secret in `global-secrets` differs from what every app reads.
  This failed `sandbox-label` on PR #49 (`ntfy.phone-token`: `TestNtfyUsers` and three alert tests got 401) and,
  before PR #47, zot's `registry.ci`.
- What reads the copies in `global-secrets`: the tests (`ntfyToken`, `registry.ci` in `gateway_test.go` and
  `sso_test.go`, `grafana.admin` in `secrets_test.go`, `test/global_secrets_test.go`), and the docs that tell people
  to read a password there (`alerts.md`: ntfy's `phone`; `post-installation.md`, `sandbox.md`: paperless, Grafana,
  zot's `ci`). No app reads them. On a restore (step 11) they would be regenerated and differ from OpenBao too.
- ESO 2.12's PushSecret takes `spec.selector.generatorRef`: each reconcile calls the generator once and, with
  `IfNotExists`, writes only the destinations that do not exist (`SecretExists`), all from that one value.
- OpenBao's role `writer` is bound to namespace `global-secrets` (`platform/openbao/values.yaml`, an `initialize`
  stanza: applied only on an empty storage).
- One entry has two destinations: `ntfy.alerts-token` (`ntfy/generated/alerts-token`, read by ntfy;
  `monitoring-system/generated/ntfy`, read by Alertmanager). OpenBao's policy `apps` lets a namespace read only its
  own branch.

## Decisions

1. **Rename** `platform/global-secrets` to `platform/generated-secrets`, namespace `generated-secrets`: it holds only
   `Password` generators, PushSecrets and the SecretStore `openbao-writer` (role `writer`, the namespace's `default`
   service account). OpenBao's role `writer` is bound to `generated-secrets`. Production does not exist yet; the
   sandbox is rebuilt (below).
2. **No intermediate Secret:** each entry is a `Password` generator and a PushSecret with `selector.generatorRef`
   (that generator), `updatePolicy: IfNotExists`, `deletionPolicy: None`, `refreshInterval: 1h`, one `data` item per
   destination (`secretKey: password`, `remoteRef.property: <key>`). No ExternalSecret, no Secret in the namespace. A
   missing destination (an OpenBao that lost its data, a rotation) is written within the hour or at a force-sync; an
   existing one never changes.
3. **One value per entry, copied per namespace in one push:** an entry's destinations are written by the same
   reconcile, so they hold the same value. A destination added to an existing entry later would get a new value: the
   docs say to copy the existing value there with the admin role first (the push then leaves it). No cross-namespace
   read in OpenBao's policy.
4. **OpenBao is the only source:** the tests read generated values from OpenBao (role `writer` reads
   `*/generated/*`, as it must before writing) or from the app's own Secret; the docs tell people the same (`bao kv
   get` with the admin role, or the Secret the app's own ExternalSecret writes in its namespace).
5. **Rotation** (`docs/concepts/secrets-management.md`) becomes: with the admin role, delete every destination of the
   entry (`bao kv metadata delete`), force-sync its PushSecret, force-sync the readers' ExternalSecrets, restart the
   apps, and change admin passwords inside the app. Roadmap 11b automates it.
6. **Values stay** in `values.yaml` as today (`name`, `key`, `length`, `special`, `destinations`, `extraSecrets`).

## Migration

- Sandbox: `make dev-fresh` once (CI always builds fresh). On an existing sandbox the old namespace keeps its
  resources (`preserveResourcesOnDeletion`) and OpenBao's role `writer` still names `global-secrets`; the docs say
  so.
- Production: nothing to migrate (step 12).

## Failure handling

- A generator or OpenBao error: the PushSecret reports it (`Errored`) and retries; an existing destination is never
  touched, so no app sees a new value.
- OpenBao loses its data without a backup: every generated secret is generated again at the next push (as today);
  step 11 backs OpenBao up.

## Tests

- Offline (basic checks, replacing `test/global_secrets_test.go`): `platform/generated-secrets` rendered has, for
  every entry, a `Password` generator and a PushSecret with `generatorRef` to it, `IfNotExists`, `deletionPolicy:
  None`, one `data` item per destination with `secretKey: password` and the entry's key as property, and no
  ExternalSecret or Secret; `scripts/check-secret-stores` knows the new name.
- Sandbox (`Secrets` group): namespace `generated-secrets` has no Secret; every destination exists in OpenBao; two
  force-syncs of a PushSecret leave OpenBao's value unchanged (hashes compared, never printed); both destinations of
  `ntfy.alerts-token` are equal. The tests that read generated values (ntfy, zot's `ci`, Grafana's admin) read them
  from OpenBao.

## Documentation

- `docs/concepts/secrets-management.md` (the generated secrets section, rotation, adding a destination), `alerts.md`,
  `post-installation.md`, `sandbox.md`; CLAUDE.md: 8e done, the sandbox paragraph and the production checklist where
  they name `global-secrets`.

## Out of scope

- Rotation commands and schedules (11b), backups (11a), cross-namespace reads in OpenBao.
