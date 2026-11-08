# Public surface

What the internet reaches, and how it is guarded (step 15f, `docs/superpowers/specs/2026-11-06-public-surface-design.md`).
The Cloudflare Tunnel publishes the public apps only; every admin app (Argo CD, Grafana, Kiali, Forgejo, Woodpecker,
zot, Kanidm) stays on the LAN Gateway.

| Host | App | Namespace |
|---|---|---|
| `www.` | blog | `blog` |
| `draw.` | excalidraw | `excalidraw` |
| `chat.` | Element web | `matrix` |
| `ntfy.` | ntfy | `ntfy` |

Three guards:

- **The allow-list.** Gateway `istio-system/public` admits routes only from the namespaces of the public map
  (`public` in `system/istio-system/values.yaml`); a route from any other namespace stays `NotAllowedByListeners`.
  `TestPublicRoutes` (basic checks) renders production and fails a route on `public` from a namespace off the map,
  with a host the map does not list for it, or without `external-dns.kubernetes.io/cloudflare-proxied: "true"`: a
  Tunnel host works only proxied (external-dns makes records DNS-only otherwise), and only then gets the WAF and the
  rate limit. `TestGatewayPublicAllowList` proves the Gateway's refusal in the sandbox.
- **Cloudflare's edge** (`infra/_modules/cloudflare`, applied by `infra/production/cloudflare`): one rate limit and the
  WAF's managed rules, below.
- **A weekly scan** (`system/public-scan`): nuclei against the public hosts from inside the cluster, below.

## Publishing an app

1. Its route: `parentRefs` `istio-system/public`, its host `<label>.{{ .Values.global.baseDomain }}`, and the annotation
   `external-dns.kubernetes.io/cloudflare-proxied: "true"`.
2. Its namespace and host label in `public` (`system/istio-system/values.yaml`).
3. The label in `targets` (`system/public-scan/values.yaml`; `TestPublicScanTargets` keeps the two equal).

The pull request then shows the new surface in one place. An admin app never goes there: publish it on the LAN Gateway,
and reach it from outside through the VPN.

## Cloudflare's edge

The Free plan (Cloudflare's docs, read 2026-11-06) allows one rate-limiting rule, counted per client IP over 10 seconds,
blocking for 10 seconds, whose expression may use only the path: it cannot name a host. So the rule
(`cloudflare_ruleset.rate_limit`) covers the whole zone: **200 requests per 10 seconds per IP**
(`rate_limit_requests`), far above a browser's cold load of Element web. Nothing else in the zone is proxied; a proxied
host added later outside the Tunnel gets the same rule. Per-account limits stay the apps' own (ntfy's per-visitor
limits read `CF-Connecting-IP`). The rule also counts cached assets: the Free plan cannot exclude them.

The WAF's managed rules are the Free Managed Ruleset. Cloudflare creates no entry point for it on a Free zone (a plan
on 2026-11-07 found none, and the dashboard's WAF page offers only paid add-ons), so the module declares the zone's
`http_request_firewall_managed` entry point (`cloudflare_ruleset.managed`) running it. Its id is not published: the
module finds it by name (`Free`) among the managed rulesets the zone may run, and a plan fails, naming those it found,
when there is not exactly one.

Apply with the rest of production (`just infra deploy production`), or the unit alone:

```sh
cf auth login          # Account API Tokens enabled on its consent page
terragrunt --working-dir infra/production/cloudflare plan
```

## The weekly scan

`system/public-scan`'s CronJob runs Sunday at 04:00 (`global.timezone`). For each host of `targets`, through public DNS
and Cloudflare, as an outsider's requests arrive:

1. **Preflight**, right before the host's scan and again right after it: the host's first page must answer
   normally. An error, a block, or a Cloudflare challenge (a 403, or a "Just a moment..." page) fails the run: the
   scan would see Cloudflare, not the app, and a challenge that began mid-scan would otherwise end as a clean run.
2. **nuclei** (`image`) with a pinned templates release (`templates`: a tarball checked against its sha256), throttled
   to `rateLimit` (8 requests per second, under half the rate limit: `TestPublicScanRateBelowLimit`), so a run never
   blocks the household. About 29 000 requests a host, an hour each. Excluded: `dos`, `fuzz` and `intrusive` templates,
   and out-of-band ones (`-no-interactsh`: they would send probes' data to ProjectDiscovery's servers). The WAF blocks
   some probes: the protection working, not a failure.
3. A run that loaded fewer than `minTemplates` templates (90% of the release's count) fails, and so does one with a
   finding the report cannot read.

The Job's log holds one line per finding, `{"public_scan":"finding","template":...,"host":...,"severity":...}`, and a
last `{"public_scan":"done","templates":N}`. nuclei's own output (its requests as curl commands, what templates
extracted) stays in the pod.

| Alert | Means | Do |
|---|---|---|
| `PublicScanFinding` (Loki) | A finding at medium or above that nobody accepted | Read the template (its id names it in the [templates](https://github.com/projectdiscovery/nuclei-templates)), then fix the app or accept it |
| `PublicScanFailed` | The newest run failed | `kubectl -n public-scan logs job/<job>` names the host it could not see, or the count |
| `PublicScanMissing` | No run succeeded for eight days | The runs fail, or the pod cannot start |

Info and low findings (technology detection, missing headers) stay in the log only.

### Accepting a finding

In `system/public-scan/values.yaml`'s `accepted` (or `values-dev.yaml` for the sandbox only), one entry per template and
host, with the reason:

```yaml
accepted:
  - template: <template-id>
    host: draw
    reason: why it is not a risk here
```

It hides that template on that host only. `TestPublicScanAccepted` refuses an entry without a reason or for a host the
scan does not have.

### A Cloudflare challenge

When the preflight reports `did not answer normally` (a 403) or `Cloudflare challenged the scan`, find out why before loosening anything: Cloudflare saw the
home connection as hostile (a shared CGNAT address with a bad reputation, or the rate limit). The scan never gets a way
around the edge: it would then test a surface no outsider sees.

### Bumping nuclei or its templates

Pin the image's digest, the templates' tag and its tarball's sha256
(`curl -sSL https://github.com/projectdiscovery/nuclei-templates/archive/refs/tags/<tag>.tar.gz | sha256sum`), and set
`minTemplates` to 90% of the new count (a run's last line, or `TestPublicScan`'s log).

## In the sandbox

The CronJob runs there against the public map's hosts the sandbox serves (`draw`, `ntfy`), through the LAN Gateway, at
150 requests per second (`values-dev.yaml`: no Cloudflare). `TestPublicScan` runs it once: it completes, loads its
templates, finds nothing at medium or above, and Loki's ruler holds `PublicScanFinding`. What Cloudflare does to the
scan is seen only in production.
