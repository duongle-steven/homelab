# Step 7b: environment settings from `infra/<env>/`

Status: design approved in chat 2026-10-16, written spec awaiting review. Branch: `feat/env-settings` (PR into `dev`).

## Goal

The domain, the time zone and the seed repository come from `infra/<env>/cluster/config.json`, like the addresses
(step 6d-1). No chart or app file names a domain; `values-dev.yaml` files keep only real sandbox differences (dev
CA, dev issuer, smaller resources). Someone else deploys their own homelab by editing `infra/<env>/`; nothing
rewrites the code, and `scripts/configure` is removed.

## Context

- Today the production domain `khuedoan.com` is written in 14 chart and app files, and 7 `values-dev.yaml` files
  only override it with `127-0-0-1.nip.io`. The app files already use `{{ .Values.global.domain }}` from an
  ApplicationSet parameter. The time zone is in `system/kured` and the PXE role; the seed repository in
  `platform/forgejo` (seed Job), `system/argocd/values-seed.yaml` (repoURL) and `apps/homepage.yaml`.
- Spike (2026-10-16): rendered each chart with its domain replaced by `{{ .Values.global.domain }}`:
  - our own wrapper templates (istio-system's Gateway and Certificate, every `KanidmOAuth2Client`, kanidm's
    `Kanidm`, routes we template) can read a global directly;
  - subcharts that run `tpl` on the values that hold the domain: grafana (`grafana.ini`, route), cloudflared,
    forgejo's route, argo-cd's `global.domain`;
  - subcharts that do not: forgejo's `gitea.config` (`app.ini`) and OAuth `autoDiscoverUrl`, zot's config, argo-cd's
    `configs.cm` (`oidc.config`) and route hostnames, woodpecker's env, renovate's config, matrix's dendrite config.
  - argo-cd has its own `global.domain` (a full host name): a parent `global.domain` overrides it, as Istio's
    `global.network` did in 6d-1. Our globals need names no subchart uses.

## Decisions

| Topic | Decision |
|---|---|
| Source | `infra/<env>/cluster/config.json` gains `domain`, `timezone`, `seed_repository` |
| Globals | `global.baseDomain`, `global.timezone`, `global.seedRepository`, passed by both ApplicationSets' `valuesObject` (app files move from the `global.domain` parameter to `global.baseDomain`) |
| Host names | Components keep a short host label (`host: grafana`) and build `<label>.<baseDomain>` |
| Subcharts with `tpl` | Values write `{{ .Values.global.baseDomain }}` |
| Subcharts without `tpl` | The wrapper chart renders that piece itself and hands it over through the subchart's existing-object input (a Secret, ConfigMap or extra config source), or disables the subchart's object and templates its own (argo-cd's route) |
| Seed repository at bootstrap | `infra/_modules/argocd` substitutes `config.json`'s `seed_repository` for a placeholder in `values-seed.yaml` when it renders the seed |
| Guard | `scripts/check-network-config` (renamed `scripts/check-env-config`) also fails on a domain literal in charts and app files, on a rendered domain that is not the environment's, and on a global of ours that a subchart also defines |

## Design

### Config and globals

`infra/production/cluster/config.json` gains `"domain": "khuedoan.com"` (a placeholder until the user's domain
exists; changing it is then a one-line edit), `"timezone": "Asia/Ho_Chi_Minh"` and
`"seed_repository": "https://github.com/duongle-steven/homelab"` (this fork; upstream's would seed the wrong code). The sandbox's has
`127-0-0-1.nip.io`, the same time zone and `https://github.com/duongle-steven/homelab`. `scripts/env-values`
resolves the new keys; both ApplicationSets add `baseDomain`, `timezone` and `seedRepository` next to `addresses`.

### Per component

| Component | Change |
|---|---|
| istio-system | Templates use `global.baseDomain` (Gateway listeners, Certificate, public target, CoreDNS rewrite); `domain` keys removed from values |
| argocd | `argo-cd.global.domain: 'argocd.{{ .Values.global.baseDomain }}'` (templated by the chart); `oidc.config` and the other `argocd-cm` keys rendered by a wrapper template with the chart's `configs.cm.create: false`; the chart's HTTPRoute disabled and a wrapper `HTTPRoute` instead; the `apps` ApplicationSet's `global.domain` parameter replaced by `valuesObject` `baseDomain` |
| grafana | `grafana.ini` and route use `{{ .Values.global.baseDomain }}`; `host: grafana` label for the Kanidm client |
| forgejo | route via `tpl`; the `[server]` section and the OAuth discovery URL from a wrapper Secret given to the chart's `gitea.additionalConfigSources` (or its documented equivalent found while implementing); seed Job reads `global.seedRepository` |
| zot | its `config.json` rendered by a wrapper ConfigMap mounted in place of the chart's (chart's `mountConfig` off) |
| kanidm | the `Kanidm` resource's domain from `global.baseDomain` |
| kured | `timeZone` from `global.timezone` (through `tpl` if the chart supports it, else a wrapper override found while implementing) |
| cloudflared, woodpecker, renovate, matrix (production only) | same rules; only rendered |
| apps | `global.domain` → `global.baseDomain`; `blog`'s image registry and `homepage`'s links use it; upstream's personal links (the docs site `homelab.khuedoan.com`) become the repository link |

Every `values-dev.yaml` loses its domain overrides; a file left empty is deleted.

### Guard

`scripts/check-network-config` becomes `scripts/check-env-config` (one script for the environment's settings) and
also fails when:

- a chart or app file (`system/`, `platform/`, `apps/`) contains `khuedoan.com` or `127-0-0-1.nip.io` (comments
  excepted);
- a production render contains the sandbox's domain or a sandbox render the production one;
- a key we pass under `global` (`addresses`, `baseDomain`, `timezone`, `seedRepository`) is also a key of a subchart's
  default `global` values.

## Error handling

- A missing key in `config.json`: `scripts/env-values` fails (6d-1), and the ApplicationSet's `missingkey=error`
  generates nothing.
- A subchart that later stops templating a value: the guard's render check sees the literal `{{` or the wrong domain.

## Testing

- Offline: `scripts/check-env-config` RED before (the literals) and GREEN after; renders of every changed component
  in production and the sandbox; the existing checks.
- `make dev-clean; make dev`: every test passes (they use the sandbox host names, so a wrong domain anywhere in
  Argo CD, Grafana, Forgejo, zot or Kanidm fails them).

## Docs

- `CLAUDE.md`: roadmap 7b done; rules: domain, time zone and seed repository live only in `infra/<env>/cluster/config.json`
  (`global.baseDomain`, `global.timezone`, `global.seedRepository`); the production checklist drops "Run `make
  configure`" for "set `infra/production/cluster/config.json`".
- `docs/installation/production/configuration.md`: edit `config.json` instead of running `make configure`.
- `scripts/configure` removed; the Terraform Cloud workspace of `external/` stays until 7c (noted).
