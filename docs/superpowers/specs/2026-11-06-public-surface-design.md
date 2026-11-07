# Step 15f: the public surface

Status: designed in chat 2026-11-06. The roadmap line came from the OWASP check in chat 2026-10-25 ("Cloudflare Access in front of the admin
apps published through the Tunnel, rate limits, a periodic nuclei/nmap scan of what the Tunnel exposes"); reading the code changed it, as below.
Builds on 7c (`infra/_modules/cloudflare`), 15c (Loki's ruler) and 15d (scans that fail when they did not really run). Decisions marked *(user)*
were made in chat; *(Claude's call)* marks the builder's own, open to review.

## Intent

The Cloudflare Tunnel exists for the public apps only *(user)*. The admin apps (Argo CD, Grafana, Kiali, Forgejo, Woodpecker, zot, Kanidm) stay
on the LAN Gateway. This step makes sure it stays that way, puts Cloudflare's edge protections in front of what is public, and looks at the
public hosts the way someone outside does, on a schedule.

What is public today, through Gateway `istio-system/public`:

| Host | App | Namespace | Proxied annotation |
| --- | --- | --- | --- |
| `www.` | blog | `blog` | yes |
| `draw.` | excalidraw | `excalidraw` | yes |
| `ntfy.` | ntfy | `ntfy` | **no** |
| `chat.` | Element web (`apps/matrix`; the host is written in full in `apps/matrix/values.yaml`'s `elementHostname`) | `matrix` | yes |

Two gaps found while reading:

- Gateway `public`'s listener has `allowedRoutes.namespaces.from: All`: one `parentRefs` edit puts any app, an admin one included, on the
  internet, and nothing checks it.
- ntfy's route lacks `external-dns.kubernetes.io/cloudflare-proxied: "true"`, so external-dns makes its record DNS-only: a Tunnel host that is
  not proxied does not work, and would skip the WAF and the rate limit anyway.

## Goals

1. Only an allow-list of namespaces can attach routes to Gateway `public`, enforced by the Gateway itself and checked offline in the basic checks.
   Every route on `public` is proxied by Cloudflare.
2. Cloudflare's WAF managed rules and a rate limit protect the public hosts, declared in OpenTofu (`infra/_modules/cloudflare`).
3. nuclei scans the public hosts on a schedule from inside production, through public DNS and Cloudflare, and alerts on a finding nobody
   accepted and on a scan that did not really run.

## Non-goals

- **Cloudflare Access** *(user)*: no admin app goes through the Tunnel, and the four public apps are meant for anyone (ntfy's phone apps and
  Element could not pass Access anyway). Access comes back only if something admin-like is published: Kanidm goes through the Tunnel in step 13
  for Headscale, and that step decides.
- **nmap** *(user)*: the Tunnel opens no port at home (CGNAT on top), so nmap would only see Cloudflare's edge. It moves to step 13, against the
  VPS's public address.
- Paid Cloudflare features (more rate-limit rules, the full managed ruleset, Bot Management), other zone settings, and hosts outside the Tunnel.
- Matrix: Element web is public while dendrite's route is on the LAN Gateway, so Element cannot sign in from outside. Recorded as a deferred
  minor, not fixed here.

## Design

### 1. The allow-list

- One map, `public` in `system/istio-system/values.yaml`, from namespace to its public hosts' first labels: `blog: [www]`,
  `excalidraw: [draw]`, `ntfy: [ntfy]`, `matrix: [chat]` *(Claude's call: a map, so the Gateway, the offline test and the scan's targets read one
  place)*. Gateway `public`'s listener uses `from: Selector` on `kubernetes.io/metadata.name In <the map's keys>`, as the LAN Gateway's SSH
  listener already does for `forgejo`. A route from any other namespace stays `Accepted: False` (`NotAllowedByListeners`).
- An offline test in `test/` (run by `scripts/ci-check`) renders every chart and app file for production and fails when a route attaches to
  `public` from a namespace not in the map, with a hostname the map does not list for it (`<label>.<baseDomain>`), or without the proxied
  annotation. Adding a public app is one line in the map plus its annotation, reviewed in its pull request.
- ntfy's route gets the annotation.
- Sandbox proof: the sandbox renders Gateway `public` too. A test creates a route in a namespace off the list and checks the Gateway refuses it,
  and that a listed namespace's route is accepted.

### 2. Cloudflare's edge, in OpenTofu

In `infra/_modules/cloudflare`, applied by `infra/production/cloudflare` (state on R2):

- **WAF managed rules**: the Free plan's managed ruleset, deployed on the zone and declared, so its state is visible and drift shows in a plan.
  The builder checks with Cloudflare's docs and provider 5.x how a Free zone deploys it (it may already be on by default, in which case the
  declaration only asserts it).
- **Rate limit**: the Free plan allows one rule, counted per client IP over a short period (the builder confirms the current limits: period,
  mitigation time, fields allowed in the expression). It likely cannot match the hostname, so the rule and the managed rules cover the whole
  zone: nothing else in `hoangduong.dev` is proxied, now or planned *(user)*. A proxied host added later outside the Tunnel gets them too; the
  guide says so. The rule blocks, and its threshold is generous enough that a browser's first load of Element web passes *(Claude's call)*:
  per-account limits stay the apps' own (ntfy's per-visitor limits, which read `CF-Connecting-IP`).
- The API token `cf auth login` gives OpenTofu may lack the rulesets permission: the builder finds out with a plan first and says so before
  asking for more.
- **Applying is the user's call** *(user)*: this touches the production zone. No public app runs yet (production is not deployed), so applying it
  now affects nothing live. The step's proof is a plan, then an apply after the user agrees, then reading the rules back.
- `make -C infra check` covers the new resources as it does the module's others.

### 3. nuclei on a schedule

- A CronJob in a new component `system/public-scan` *(Claude's call: in the cluster rather than GitHub Actions, so a finding alerts through
  Alertmanager like everything else, and the requests leave from the home connection as an outsider's would reach Cloudflare)*. Weekly.
- Targets: `https://<label>.<baseDomain>` for each host in the map (not typed twice).
- The scan goes through the WAF and the rate limit like anyone else's requests *(user)*: no header or rule lets it past them, since that would
  scan a surface no outsider sees and add a secret to rotate. nuclei is throttled below the rate limit's threshold, so it never blocks the
  household's own requests; a weekly run may take hours. Probes the WAF blocks are the protection working, not a failure. nuclei's templates are a pinned
  release, checked, not fetched as "latest" at run time. Image pinned per the version rule.
- Severities `medium` and above are findings *(Claude's call)*; `info` and `low` go to the log only.
- Accepted findings: a list beside the CronJob's values (template ID, host, reason), like 15e's accepted CVEs. An entry without a reason fails the
  basic checks.
- Output: JSON lines to stdout, read by Alloy into Loki. Loki's ruler alerts on:
  - `PublicScanFinding`: a finding not accepted. The alert names the template ID and host only.
  - `PublicScanMissing`: no completed scan in the last 8 days, or a scan that did not really run: some host's first, ordinary request did not
    get the app's normal answer (an error, a Cloudflare block or challenge page) *(user)*, or nuclei ran fewer templates than a floor the builder
    sets from a first run.
- Egress: the namespace gets 443 out (`system/egress/values.yaml`), its own `AuthorizationPolicy` and the rest a component needs
  (`test/scopes.yaml`, policy securityContext).
- Sandbox: the CronJob runs there against the map's hosts the sandbox serves (excalidraw and ntfy; blog and matrix do not run there), through
  the LAN Gateway they attach to (`*.127-0-0-1.nip.io`, the local CA), so the job,
  its parsing and both alerts are proven. A test triggers it once and checks it completed and found nothing unaccepted. What Cloudflare does to
  the scan is seen only in production.

## Constraints

- Latest stable everything, pinned (nuclei, its templates, providers).
- Never print Cloudflare tokens, the zone id or the account id.
- Never apply to production without the user's go-ahead.
- Alerts and reports name only template IDs and hosts, never response bodies.

## Done means

- The allow-list is enforced on Gateway `public`, the offline test and the sandbox test pass, and ntfy is proxied.
- The WAF and rate-limit resources plan cleanly; applied and read back once the user agrees.
- The scan CronJob runs in the sandbox, its alerts load, and the guide (`docs/how-to-guides/public-surface.md`) says how to read and accept
  findings and how to add a public app.
- CLAUDE.md: 15f's line rewritten (no Access, nmap moved to step 13), the before-production list says what ran only as a plan or only in the
  sandbox (the rules' effect on real traffic, the scan through Cloudflare).
