# The homelab's threat model

Status: a draft for review, 2026-11-14. The user asked for it in chat on 2026-11-14: review what is or will be exposed (Kanidm for Headscale,
Matrix, the step-13 VPS) before it is, rather than one service at a time. Decisions marked *(user)* were made in chat; *(Claude's call)* marks
proposals open to review. Nothing here is built yet: each gap names the step that would close it.

## Intent

One place that says what the homelab protects, from whom, where an attacker could come in, what already stands in the way, and what is
missing, so that each later step (OpenTelemetry, Backstage, the blog, 12b-3, 13, 18b) is checked against it instead of discovering its own
risks late. It is a living document: a step that adds an entry point updates it.

## What is worth protecting

| Asset | Where | Losing it means |
| --- | --- | --- |
| Every secret | OpenBao (raft on a PVC), its unseal key in production's encrypted state on R2 | everything below |
| Personal data | Actual Budget, paperless, OpenCloud, Matrix, (Immich, step 17) | privacy of the family's documents, money, files, chats |
| Identity | Kanidm (people, groups, OAuth2 clients) | every app's sign-in |
| The deploy path | GitHub `master` → `lab infra forgejo push` → `ops/homelab` → Argo CD (cluster-admin) | the whole cluster |
| The signing key | OpenBao transit `ci-signing` | what Kyverno trusts as built by the golden path |
| Backups | R2 (Velero, Kopia), the OpenBao snapshot | recovery after any of the above |
| The home network | the Archer C64, the family's devices | a foothold beyond the homelab |

## Who it is protected from

- **The internet at large** (scanners, botnets, credential stuffing): the realistic threat for anything public.
- **A compromised dependency** (an image, a chart, a GitHub Action, a Renovate update): the supply chain.
- **A device on the home LAN** (a compromised laptop, phone or IoT device): the network is flat today.
- **A member of org `ci` or of `homelab-editors`** acting beyond their role, or whose account is taken over.
- **Later, strangers running code on purpose** (the teaching app, 18b).

Out of scope *(user, 2026-11-14)*: a targeted attacker with physical access to the house (the disks are unencrypted, an accepted risk since
15d), and the providers themselves (Cloudflare, GitHub, AWS, Kanidm's and Argo's maintainers).

## Entry points

| # | Entry point | Today | Planned |
| --- | --- | --- | --- |
| E1 | Public apps through the Cloudflare Tunnel (blog, excalidraw, ntfy) | only rendered (12b-3) | Kanidm for Headscale (13); Matrix if federated |
| E2 | The LAN Gateway (every admin app, Kanidm, Forgejo SSH) | sandbox | production (12b-3) |
| E2c | Backstage (Backstage-1, LAN Gateway only): homelab-admins and homelab-editors sign in with Kanidm; it reads the cluster (no Secrets, ConfigMaps, logs or exec), Argo CD (read-only) and the repository Argo CD syncs; its token creates repositories in org `ci` | sandbox (Backstage-1) | production once its image is released |
| E2d | Kargo (app delivery, LAN Gateway only): homelab-admins administer and homelab-editors view through Kanidm (public client, PKCE); its controller writes Forgejo's `ops/releases` as user `kargo` and syncs the Applications annotated for it | sandbox (app delivery) | production |
| E2b | OTLP into the gateway Collector (traces from the Istio Gateways, Argo CD, Grafana) | sandbox (OpenTelemetry step) | inside the cluster only: `allow-otlp` names the senders, nothing routes to it |
| E3 | The nodes' own ports from the LAN (SSH, the API server, etcd, the kubelet, flannel's VXLAN) | production's nodes | |
| E4 | The step-13 VPS (SSH, WireGuard, Headscale, DERP/STUN on a public address) | none | 13 |
| E5 | The supply chain (images, charts, Actions, Renovate's pull requests) | GitHub, the sandbox | |
| E6 | The in-cluster CI running a Containerfile (org `ci`) | sandbox (10f); Backstage's template creates repositories there (admins only, Backstage-1) | 18b's learners |
| E7 | The workstation's admin path (admin kubeconfigs, the state passphrase, SSH keys, Cloudflare and GitHub sessions) | in use | |
| E8 | Accounts outside the homelab (GitHub, Cloudflare, AWS, the domain's registrar) | in use | AWS (13) |

## What already stands in the way

- **No inbound port at home** (CGNAT, double NAT): the internet reaches the homelab only through the Tunnel, which connects outbound.
- **The public surface (15f):** Gateway `public` admits only an allow-list of namespaces and hosts, every public route is proxied, Cloudflare's
  WAF managed rules and one zone rate limit, nuclei weekly from inside. ntfy denies by default, has no sign-up and no attachments.
- **Identity:** Kanidm OIDC with PKCE for every app; apps with personal data admit only their access group.
- **Inside the cluster:** ambient mesh with strict mTLS and default-deny `AuthorizationPolicy` per namespace (6b, 6c), default-deny egress for
  pods (6d-2), Pod Security `baseline` enforced and `restricted` audited by Kyverno (15b-1), `seccomp-default` (15h), secrets encrypted at rest
  in etcd (15d), OpenBao behind ESO with per-namespace stores (8a).
- **Supply chain (15a, 15e):** pinned versions, gitleaks, Trivy on new images in pull requests, the Trivy Operator on running images, lab's image
  signed, 10f's images signed, zizmor on the workflows, push protection on GitHub.
- **Deploy path (10c):** production deploys only `origin/master` whose checks and sandbox passed, pushed by a user only the workstation holds.
- **Detection (15c, 15g):** the API server's and OpenBao's audit logs with alerts, Falco, the Trivy alerts, every alert to ntfy and Telegram.
- **Recovery (11a, 11b):** daily off-site backups with a write-only R2 token, restores only by command, every new cluster read-only until
  `lab backup enable`; every secret rotatable by its official method.

## Gaps

Ordered by what a person running this homelab would lose, most first. Each names a proposal and the step that would carry it.

### G1. Kyverno only audits (15b-2 not done)

Pod Security `restricted`, pinned images, allowed registries and the image signatures (`verify-lab-image`, `verify-ci-images`) report but
block nothing. 10f's final review showed the cost: the build pod broke a restricted rule and nothing noticed until a reviewer read it.

The window is small, though: every workload from git already passes the same policies in the pull request's basic check `policy`, Pod
Security Admission enforces `baseline` (privileged, host paths and namespaces, dangerous capabilities) whatever Kyverno says, and the native
admission policies (the build's seccomp, `ci-workflow-arguments`) refuse on their own. What Audit leaves open is what no render shows (pods an
operator makes: Ceph, CloudNativePG, Trivy's scans, the golden path's steps) and the image signatures, which only someone already able to
create pods could get around.

Decision *(user, 2026-11-14)*: production starts in Audit (12b-3) and switches to Enforce (15b-2) after a short window of reading its own
PolicyReports: production runs workloads the sandbox never does (cloudflared, the VPN routers, jellyfin, Ceph's daemons, kube-vip, Renovate's
initContainer), whose reports exist nowhere else, and the `skip` results show which `PolicyException`s are still used and which can go.
Enforcing before 12b-3 would risk refusing those pods on the first deploy. During the window:

- an alert on a new `fail` result in a PolicyReport (today only `PolicyException` changes outside Argo CD alert), so the window does not depend
  on someone reading the reports;
- nothing new is published and nobody outside the household joins org `ci`;
- Enforce comes before step 13 at the latest, and within about two weeks when the reports are clean; it also needs who may create a
  `PolicyException` restricted, the golden path's pods rendered for the policy check (10f's deferred minor) and the first release's signatures
  (lab's image) verified.

### G2. The nodes' cluster ports are open to the whole LAN (E3)

`infra/_modules/metal/flake/profiles/k3s-server.nix` opens 2379 and 2380 (etcd), 10250 (the kubelet) and UDP 8472 (flannel's VXLAN) to any
address; only the metrics ports are limited to the nodes and the pod network. etcd and the kubelet require client certificates, but flannel's
VXLAN is neither encrypted nor authenticated: any LAN device that sends to UDP 8472 can inject packets into the pod network as if from a pod,
around the mesh's policies for anything outside it. SSH and the API server (6443) are open to the LAN too, which is how the workstation reaches
them.

Proposal *(Claude's call)*: limit 2379, 2380, 10250 and 8472 to the nodes' addresses (and 10250 to the pod network for the metrics scrape),
like the metrics ports already are; set SSH to keys only explicitly (`PasswordAuthentication` and `KbdInteractiveAuthentication` off: no user
has a password today, which is all that stops it). A small change to `infra/_modules/metal`, proven on the testbed (it gates releases there
anyway). Restricting 6443 and SSH to the workstation needs a fixed admin address: it comes with G6.

### G3. Kanidm on the internet (step 13, E1)

Headscale's OIDC sign-in needs Kanidm reachable from the internet, through the Tunnel. Kanidm is the key to every app, and its admin API shares
the host with its login.

Proposal *(Claude's call)*, all preconditions of publishing it:
- passkeys only for every person (Kanidm's credential policy on the groups: no password, no TOTP-only sign-in); today no policy is set;
- only the paths Headscale's OIDC flow needs on the public host (a path allow-list at Gateway `public`, as 15f's host allow-list), the rest
  (admin API, account recovery) LAN only;
- alerts on Kanidm's failed sign-ins from Loki, as 15c does for OpenBao;
- a rate limit on its sign-in paths: the Free plan's one rule already covers the whole zone, so either a separate host with its own rule (a
  paid plan) or a limit in Kanidm or at the Gateway;
- whether Cloudflare Access goes in front of it (13 already asks), weighed against signing in twice.

### G4. The VPS (step 13, E4)

The first machine with a public address, and the WireGuard hub that routes back into the home.

Proposal *(Claude's call)*: SSH reachable only through the tailnet (no public port 22), the VPS's own firewall (nftables) with only WireGuard,
Headscale and DERP/STUN, Headscale ACLs by least privilege, the hub's `AllowedIPs` towards the home limited to the Gateway's VIP (not the whole
LAN), automatic NixOS updates, and the periodic nmap 13 already plans. The VPS holds no secret that opens the cluster.

### G5. The flat home network (E3)

The nodes share the LAN with the family's phones, laptops and IoT devices: a compromised device reaches every node port (G2) and the LAN
Gateway's admin apps (behind Kanidm), and a compromised node reaches every device.

Proposal *(user, 2026-11-14)*: OPNsense is not worth it for the public apps (no inbound port; the Tunnel connects outbound), but it, or a managed
switch with VLANs and OpenWrt, would separate the homelab from the home, filter the nodes' own egress and serve DNS over TLS for the whole LAN.
Recorded in Ideas, decided with the UPS or step 13's hardware.

### G6. The workstation is the strongest admin path (E7)

It holds production's admin kubeconfig (a client certificate valid for a year), the state passphrase (which guards the unseal key and the
R2 credentials), SSH keys and the Cloudflare and GitHub sessions. The 15a scan already found unencrypted Cloudflare state there (listed for
the first deploy).

Proposal *(Claude's call)*: `kubectl` through Kanidm OIDC (short-lived tokens, group-based RBAC, the audit log naming the person), with the
admin certificate kept offline for break-glass; hardware-key 2FA on GitHub, Cloudflare, AWS and the registrar (a checklist in the docs, not
code); the state passphrase out of a plain file (a password manager or `pass`). A step of its own after 12b-3.

### G7. What the in-cluster CI runs (E6)

Org `ci`'s members are trusted, but the build runs their Containerfile with zot's credentials in its environment (10f deferred minor), and any
repository may publish under another repository's name (deferred to 15b-2). Backstage's templates and 18b's learners widen who that is.

Proposal: the two deferred minors before anyone outside the household joins org `ci`; 18b keeps its own design (Ideas: sandboxed runtime,
quotas, its own nodes).

App delivery *(2026-11-19)*: an image in zot now reaches production without a pull request, so zot's writers are its
gate. Only the CI (user `ci`) and `homelab-admins` push; `homelab-editors` lost write. Kargo admits an image to
`production` only after stage `verify` checked `ci-signing`'s signature on it (Kyverno's `verify-ci-images` still only
audits until 15b-2), so an admin's hand-pushed image stops there too. User `kargo`'s token (`write:repository` on
`ops/releases` only, pushes allowed to it alone) sits in Kargo's pod: taking it lets one choose among already-signed
versions of our apps, not run an unsigned one. Pods pull our images as zot's read-only `puller`, per namespace.

Backstage-1 *(2026-11-17)*: its template is one more way to create such a repository, open only to `homelab-admins`
(the permission policy, server-side, tested) and limited to `fetch:*`, `publish:gitea`, `catalog:register` and
`debug:log`; its Forgejo user `backstage` can create repositories in org `ci` and read `ops/homelab`, and Forgejo makes
it an admin collaborator of every repository it creates. Its token sits in Backstage's pod (namespace `backstage`, ESO):
a compromised Backstage, or its token taken from the pod, can push to the default branch of every repository its
template made (the blog's included), which the golden path then builds and signs with `ci-signing`. Accepted for now
*(Backstage-1)*: Backstage is LAN only behind Kanidm and its pod holds no other credential worth more; the fix, `lab
infra forgejo sync` dropping `backstage`'s collaborator access on `ci` repositories after creation, is a deferred minor.

### G8. Detection blind spots

- Kanidm's failed sign-ins raise nothing (G3).
- Cloudflare's WAF and rate-limit events stay in Cloudflare (the Free plan has no log push): the dashboard is the only place to look.
- Falco's numbers and events on production are unmeasured (listed for the first deploy).
- Argo CD's repo server once hung 27 minutes on a stalled git fetch with every Application `Unknown` (10f deferred minor): an availability gap.

Proposal: Kanidm's alert with G3; the rest stays as listed.

### G9. Backups an attacker could reach

The R2 token Velero holds is write-only, so a compromised cluster cannot read or delete old backups through it. Whoever holds the account
(G6) can, and R2's object lock is not used.

Proposal *(Claude's call)*: R2 bucket lock rules (retention by age) on the backup bucket, set in `infra/production/backup`, if R2 supports it
on the plan; otherwise accept and rely on G6.

### G10. Matrix if it leaves the LAN

LAN only today, federation off. Making it public, or federating, exposes the server-to-server API, media uploads and invites to the internet,
on a server (tuwunel) newer and less audited than the rest.

Proposal: stays LAN only; publishing or federating it is its own step with an allow-list of servers, media size and quota limits, and
sign-in through Kanidm only (as today).

### G11. The nodes themselves, and files from outside

Added 2026-11-15 *(user)*. Falco watches the nodes' syscalls, the audit logs and kube-bench cover the rest, and NixOS keeps
the system declarative in a read-only store, so ClamAV or Wazuh on the nodes would cost more than they find (Wazuh's
manager and indexer alone need several GB). What is left:

- **Patches:** the nodes' kernel and packages come from the node flake's `flake.lock`; check that it is bumped on a
  schedule (Renovate or a recipe) and rolled out with `lab cluster upgrade`, or the nodes age silently.
- **Integrity:** a weekly `nix-store --verify --check-contents` from a systemd service and timer in `infra/_modules/metal`,
  staggered per node and at `IOSchedulingClass=idle` (it reads the whole store, on etcd's disk), writing a `.prom` file
  for node-exporter's textfile collector, with alerts on a failure and on no recent success; proven on the testbed.
- **Files from outside:** one shared `clamd` for OpenCloud's uploads, paperless's consume and Transmission's finished
  downloads (CLAUDE.md's Ideas).

## Order

*(Claude's call)*, to debate:

1. Before 12b-3 (the first production deploy): G2 (node ports, proven on the testbed), G1's alert on new `fail` results,
   and LAN DNS (Blocky, with Tailscale's split DNS: the LAN apps' names resolve nowhere in production yet, and
   publishing their private address in public DNS would leak the LAN's layout and meet routers' rebinding protection).
2. After 12b-3, within about two weeks of production's reports and before step 13 at the latest: G1 (15b-2 Enforce) *(user, 2026-11-14)*.
3. With step 13: G3 and G4, as preconditions of publishing Kanidm and starting the VPS.
4. After 12b-3: G6 (admin path) as its own step; G9 with the backup bucket's next change.
5. Ideas: G5 (segmentation), G10 (Matrix public), G11 (nodes, files from outside; patches checked before 12b-3).

## How it stays current

A step that adds or changes an entry point (a public host, a new account, a new way to run code) updates the table and the gaps in the same
pull request, as a step adds its tests to `test/scopes.yaml`. The 18a docs turn it into a decision record.
