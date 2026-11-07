# Public Surface (step 15f) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Only an allow-list of namespaces reaches the internet through Gateway `public`; Cloudflare's WAF and one rate limit sit in front of
it; nuclei scans the public hosts weekly and alerts on what nobody accepted.

**Architecture:** A namespace → hosts map in `system/istio-system/values.yaml` drives Gateway `public`'s `allowedRoutes` selector; an offline
test checks every production route against it. `infra/_modules/cloudflare` gains two `cloudflare_ruleset`s (managed WAF, rate limit), tested
with `tofu test` and a mock provider. A new chart `system/public-scan` runs nuclei from a CronJob through public DNS; a Job fails when the scan
did not really run (Prometheus alert `PublicScanMissing`), and findings go to Loki (ruler alert `PublicScanFinding`).

**Tech Stack:** Helm, Gateway API v1, OpenTofu with Cloudflare provider 5.x, nuclei, Loki ruler, PrometheusRule, Go tests in `test/`.

**Spec:** `docs/superpowers/specs/2026-11-06-public-surface-design.md`

## Global Constraints

- Latest stable everything, pinned exactly: nuclei `v3.11.1` (image by digest), nuclei-templates `v10.5.1` (tarball by sha256), Cloudflare
  provider `5.28.1` (bump from 5.28.0 in `infra/_modules/cloudflare/versions.tf`). Recheck upstream when the task starts.
- Never print Cloudflare tokens, the zone id or the account id. Never read `~/.config/homelab/state.env` or `.state/`.
- Nothing is planned or applied against production Cloudflare without the user's go-ahead (Task 6).
- Alerts and logs the repository writes name only template IDs and hosts, never response bodies or extracted values.
- Every workload sets its securityContext (RuntimeDefault, no privilege escalation, non-root, read-only root filesystem) or carries a
  `PolicyException` with its reason.
- `make dev` and every sandbox run only from `/home/nixos/homelab`, never a worktree; check `pgrep -x terragrunt; pgrep -x make` first.
- The public map: `blog: [www]`, `excalidraw: [draw]`, `ntfy: [ntfy]`, `matrix: [chat]`.

## Review Focus

1. A second route in a listed namespace with a host not in the map (e.g. `admin.` in `matrix`): the offline test must fail it, not only check
   the namespace. Test: Task 1's `TestPublicRoutes` case "listed namespace, unlisted host".
2. A LAN route in a listed namespace (dendrite on `gateway` in `matrix`): must not be flagged. Test: Task 1, case "listed namespace on the LAN
   Gateway".
3. A target answering with a Cloudflare challenge (`cf-mitigated: challenge`) or 5xx: the Job must fail, not report a clean scan. Test: Task 3's
   wrapper test cases.
4. An accepted entry for one host must not hide the same template on another host. Test: Task 3, `TestPublicScanAcceptedPerHost`.
5. The rate limit must sit above nuclei's throttle with room (at least 2×): Task 3's offline test compares the two values.

## Plan rulings

- **The scan's targets are a second list** in `system/public-scan/values.yaml`, kept equal to the map by Task 3's offline test (the spec said
  "not typed twice": Helm cannot read another chart's values, and Argo CD passing one component's values to another adds more than it saves).
- **`PublicScanMissing` is a PrometheusRule** on the CronJob's last success (as `SecretRotationOverdue`), not a Loki rule: a failed Job and a
  missing run then look the same.
- **Acceptance happens before nuclei logs**: the wrapper runs nuclei once per host with that host's `-exclude-id`s, so Loki's rule needs no list.

---

### Task 1: The allow-list on Gateway `public`

**Files:**
- Modify: `system/istio-system/values.yaml` (add `public:` map with a comment), `system/istio-system/templates/gateway.yaml` (Gateway `public`
  listener `https`), `apps/ntfy.yaml` (route annotation)
- Create: `test/public_surface_test.go`
- Modify: `test/gateway_test.go` (sandbox test), `scripts/ci-check` (`offline_tests`), `test/scopes.yaml` (skip the new offline file)

**Interfaces:**
- Produces: values key `public: map[namespace][]hostLabel`; Go helper `publicMap(t) map[string][]string` (reads it with `renderYq`-style yq on
  the values file) for Task 3; `publicRouteProblems(routes []renderedRoute, public map[string][]string, domain string) []string` where
  `renderedRoute{Namespace, Name, Gateway string; Hostnames []string; Proxied bool}`.

- [ ] **Step 1: Write the failing offline test** `TestPublicRoutes` in `test/public_surface_test.go`:
  - table cases on `publicRouteProblems` (domain `example.org`): all fine → empty; namespace off the map → one problem naming
    `<ns>/HTTPRoute/<name>`; listed namespace, unlisted host → one problem; missing proxied annotation → one problem; listed namespace on the
    LAN Gateway (`gateway`) with any host → no problem.
  - the repository: `scripts/render-all prod <tmp>`, collect every HTTPRoute whose `parentRefs` name `public` (namespace `istio-system`), with
    `metadata.namespace` (renders of apps have none: use the app file's name, `apps/matrix` → `matrix`), hostnames, and
    `external-dns.kubernetes.io/cloudflare-proxied == "true"`; `assert.Empty(publicRouteProblems(...))`; and assert the rendered Gateway
    `public`'s `https` listener is `from: Selector` with `kubernetes.io/metadata.name` `In` exactly the map's keys, sorted.
- [ ] **Step 2: Run** `cd test && go test -count=1 -run 'TestPublicRoutes' ./...` — expect FAIL (no map, ntfy unproxied, `from: All`).
- [ ] **Step 3: Implement**: the map in values (comment: what it is, that a public app adds a line here plus the annotation, guide link
  `docs/how-to-guides/public-surface.md`); the listener `from: Selector`, `selector.matchExpressions: [{key: kubernetes.io/metadata.name,
  operator: In, values: <keys sorted>}]`; `external-dns.kubernetes.io/cloudflare-proxied: "true"` on ntfy's route; `TestPublicRoutes` into
  `offline_tests`; `test/public_surface_test.go` into `scopes.yaml`'s offline skips.
- [ ] **Step 4: Run** the test again — PASS; `scripts/render-chart system/istio-system dev` still renders.
- [ ] **Step 5: Sandbox test** `TestGatewayPublicAllowList` in `test/gateway_test.go` (`gatewaySandboxOnly`): create a throwaway namespace
  `public-allow-test` (cleanup on exit) with an HTTPRoute `probe` to `istio-system/public` `https`, host `probe.<testDomain>`, backend any
  Service; wait ≤2 min for its parent status; assert `Accepted` `False` with reason `NotAllowedByListeners`. The positive side: an HTTPRoute
  `public-allow-probe` in `ntfy` to `public` is `Accepted: True` (deleted after).
- [ ] **Step 6: Commit** `feat(gateway): only listed namespaces reach the public Gateway`.

### Task 2: Cloudflare's WAF and rate limit in OpenTofu

**Files:**
- Modify: `infra/_modules/cloudflare/main.tf` (two rulesets), `variables.tf` (rate-limit inputs with defaults), `versions.tf` (5.28.1)
- Create: `infra/_modules/cloudflare/tests/edge.tftest.hcl` (mock provider, `override_data` for `cloudflare_accounts`, `cloudflare_zone`,
  the permission groups, and whatever the managed ruleset lookup reads)
- Create: `test/cloudflare_test.go` (`TestCloudflareEdgeRules` runs `tofu -chdir=... init -backend=false` and `tofu test`), add it to
  `offline_tests` and the file to `scopes.yaml` skips (`infra/_modules/cloudflare/**` is already production-only: check its scope)

**Interfaces:**
- Produces: variables `rate_limit_requests` (number, default decided in Step 1), `rate_limit_period` (number, seconds), consumed by Task 3's
  offline test through `infra/_modules/cloudflare/variables.tf` defaults (it reads them with a regexp on the file: keep each default on one
  line `default = N`).

- [ ] **Step 1: Read Cloudflare's current docs** (web-fetch): the Free plan's rate-limiting rules (count, periods, mitigation timeouts,
  counting characteristics, fields allowed in the expression) and how a Free zone gets the Free Managed Ruleset (deployable through the
  `http_request_firewall_managed` phase, or always on). Record the facts with their URLs in the task's report. Pick the threshold:
  generous for Element web's first load (at least 2× the requests a cold load of `chat.` makes; measure Element web's request count in the
  sandbox's browser-free way, `curl` of its index plus the assets it lists, or take Cloudflare's guidance) and at least twice nuclei's rate in
  Task 3 (`rateLimit` 5 requests per second → 50 per 10 s, so ≥100 per 10 s).
- [ ] **Step 2: Write the failing `tofu test`**: a `run "edge"` with `command = plan` asserting: a `cloudflare_ruleset` in phase
  `http_ratelimit`, zone scope, one rule, action `block`, `ratelimit.characteristics` containing `ip.src`, `requests_per_period` =
  `var.rate_limit_requests`, period = `var.rate_limit_period`; and the managed one (phase `http_request_firewall_managed`, rule `execute`
  of the Free managed ruleset) — or, if Step 1 found it cannot be declared on Free, a `check` block that reads the zone's managed phase and
  fails when the Free ruleset is not executed. Run `go test -run TestCloudflareEdgeRules` → FAIL.
- [ ] **Step 3: Implement** the rulesets in `main.tf`, each with a comment (what it guards, the Free plan's limit it fits, that it covers the
  whole zone because nothing else in it is proxied: spec).
- [ ] **Step 4: Run** `TestCloudflareEdgeRules` → PASS; `make -C infra check` → passes (`HOMELAB_CLOUDFLARE_OFFLINE=1`).
- [ ] **Step 5: Commit** `feat(cloudflare): the WAF's managed rules and one rate limit on the zone`.

### Task 3: The scan Job (`system/public-scan`)

**Files:**
- Create: `system/public-scan/Chart.yaml`, `values.yaml`, `values-dev.yaml`, `files/scan.sh`, `templates/cronjob.yaml`,
  `templates/configmap.yaml` (the script), `templates/prometheus-rule.yaml`
- Modify: `system/egress/values.yaml` (`public-scan: [{port: 443}]`, comment: the public hosts through Cloudflare, the templates' tarball),
  `system/argocd/values-dev.yaml` (`- path: system/public-scan` in the root list), `test/scopes.yaml` (group
  `[system/public-scan/**] → PublicScan`, add `PublicScan` to `full`), `scripts/ci-check` (`offline_tests`)
- Create: `test/public_scan_test.go`

**Interfaces:**
- Consumes: `publicMap(t)` (Task 1); `rate_limit_requests`/`rate_limit_period` defaults (Task 2).
- Produces: values `targets: [label…]`, `accepted: [{template, host, reason}]`, `rateLimit` (req/s), `minTemplates`, `image`,
  `templates: {version, sha256}`, `schedule` (`"0 4 * * 0"`, Sunday 04:00 in `global.timezone`), `devCA` (dev only); Job log lines: nuclei's
  JSONL (fields `template-id`, `host`, `info.severity`) plus one final line `{"public_scan":"done","templates":N}`; CronJob name
  `public-scan`.

- [ ] **Step 1: Write the failing offline tests** in `test/public_scan_test.go`:
  - `TestPublicScanTargets`: production `targets` equal the map's labels (same set); dev `targets` ⊆ the map.
  - `TestPublicScanAccepted`: every `accepted` entry has a non-empty `template`, a `host` in `targets`, and a `reason`; rendered args for host A
    carry A's IDs only (`TestPublicScanAcceptedPerHost`: two entries, same template, hosts `www` and `draw` with only `www` accepted → the
    `draw` invocation has no `-exclude-id`).
  - `TestPublicScanRateBelowLimit`: `rateLimit × rate_limit_period × 2 ≤ rate_limit_requests`.
  - `TestPublicScanScript` runs `files/scan.sh`'s preflight function against a local `httptest` server (the script takes `PUBLIC_SCAN_CHECK_ONLY=1`
    and a target URL list): 200 → ok; 503 → fail; 403 with header `cf-mitigated: challenge` → fail; 200 with body containing
    `<title>Just a moment...</title>` → fail. Exit code non-zero on fail, and its message names only the host.
- [ ] **Step 2: Run** them → FAIL.
- [ ] **Step 3: Implement** `files/scan.sh` (POSIX sh, busybox in nuclei's Alpine image; confirm the image has `wget`, `sha256sum`, `tar`):
  download the templates tarball, check its sha256, unpack under `/tmp`; preflight each target (status 2xx/3xx, no `cf-mitigated`, no
  challenge title), exit 1 naming the host on failure; then per target
  `nuclei -u https://<label>.<domain> -t <dir> -jsonl -silent -omit-raw -rl <rateLimit> -c 5 -etags dos,fuzz,intrusive
  -disable-update-check [-exclude-id <ids>]` (stdout to the log: Loki keeps it); count the loaded templates from nuclei's stats or
  `-tl`, and exit 1 when under `minTemplates`; last, the `done` line. `-omit-raw` keeps request and response bodies out of Loki.
  CronJob like `platform/secrets-rotation/templates/cronjob.yaml`: `concurrencyPolicy: Forbid`, `backoffLimit: 0`, `activeDeadlineSeconds`
  14400, runAsNonRoot with a numeric user (65532), `HOME=/tmp`, emptyDir `/tmp`, read-only root, drop ALL, requests `{cpu: 50m, memory:
  256Mi}` (adjust to the sandbox's measured peak), dev CA mount as `secrets-rotation` does when `devCA`. `PublicScanMissing` (PrometheusRule,
  like `SecretRotationOverdue`): last success older than 8 days, or never and created more than 8 days ago, `for: 1h`, warning; and
  `PublicScanFailed` on the newest Job failed, as `SecretRotationFailed`. Production `minTemplates` from the sandbox's first run (Task 4),
  meanwhile 1000.
- [ ] **Step 4: Run** the offline tests → PASS; `scripts/ci-check policy`, `scripts/check-mesh-policies`, `scripts/check-env-config` and
  `make -C cli check` pass.
- [ ] **Step 5: Commit** `feat(public-scan): nuclei on the public hosts every week`.

### Task 4: The finding alert and the sandbox proof

**Files:**
- Create: `system/loki/files/public-scan-rules.yaml`
- Modify: `system/loki/templates/audit-rules.yaml` (second key in the same ConfigMap, mounted in the same directory), `test/public_scan_test.go`
  (sandbox test), `test/security_logs_test.go` or the new file (rule render test)

**Interfaces:**
- Consumes: Task 3's log format and CronJob name.

- [ ] **Step 1: Write the failing render test** `TestPublicScanRuleRender` (offline): the rendered Loki ConfigMap has the key, the rule
  `PublicScanFinding` selects `{namespace="public-scan"}`, parses `template-id`, `host`, `info.severity`, keeps `medium|high|critical`, sums
  by template and host only, over `[1h]`, `> 0`, severity label `warning`, and its summary has no other label.
- [ ] **Step 2: Implement** the rule (comment: why `[1h]`: one notification per finding per run), add `TestPublicScanRuleRender` to
  `offline_tests`. Run → PASS.
- [ ] **Step 3: Sandbox test** `TestPublicScan` (sandbox only): create a Job from the CronJob (`kubectl create job --from=cronjob/public-scan`),
  wait ≤30 min for `Complete`, assert its log ends with the `done` line, `templates ≥ minTemplates`, and no medium-or-higher line; delete the
  Job. Record the run's duration, peak memory and template count in the task report, and set production `minTemplates` to 90% of the count.
- [ ] **Step 4: Run** from the main checkout: `make dev tests='TestPublicScan|TestGatewayPublicAllowList'` (WSL memory checked before) →
  PASS. A finding on the sandbox's apps is read: fixed, or accepted with its reason in `values.yaml` (and `values-dev.yaml` if dev-only).
- [ ] **Step 5: Commit** `feat(public-scan): alert on findings nobody accepted`.

### Task 5: Docs and the roadmap

**Files:**
- Create: `docs/how-to-guides/public-surface.md` (what is public and why, adding a public app: the map, the annotation, the scan's `targets`;
  the WAF and the rate limit covering the whole zone, and that a proxied host added outside the Tunnel gets them; reading a finding and
  accepting it; what `PublicScanMissing`/`PublicScanFailed` mean; Cloudflare challenge blocks the scan → read why before loosening anything)
- Modify: `mkdocs.yml` (nav), `CLAUDE.md` (15f's line rewritten as done: no Access, nmap to step 13; step 13 gains "nmap against the VPS";
  the before-production list: the rules applied but never met real traffic, the scan through Cloudflare only in production, ntfy proxied only
  rendered), `docs/superpowers/deferred-minors.md` (15f: Element public while dendrite is LAN-only → any; remove the 15d line keyed by binding
  if this step does not fix it — it does not: leave it, retarget its step to "any")

- [ ] **Step 1: Write** them; `mkdocs build --strict` passes.
- [ ] **Step 2: Run** `direnv exec . scripts/ci-check all` → passes.
- [ ] **Step 3: Run** `make dev` from the main checkout (its scope) → passes.
- [ ] **Step 4: Commit** `docs: the public surface`.

### Task 6: Production Cloudflare (with the user)

- [ ] **Step 1: Ask the user** to run the plan: `! direnv exec . terragrunt --working-dir infra/production/cloudflare plan` after
  `cf auth login` (their session; nothing printed holds a token). Expect: two rulesets to add, nothing else changed. If the token lacks a
  rulesets permission, say which and stop.
- [ ] **Step 2: With the user's go**, apply; then read back through Cloudflare's dashboard or the plan (no changes) and record it.
- [ ] **Step 3:** Update the PR body's proof.
