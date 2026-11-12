# Deferred minors

Review findings graded Minor and left open, collected from every PR (checked against the code on 2026-10-17; PRs #4
to #29 had theirs fixed in the PR itself or in the cleanup PR #29). Every PR adds the minors it defers here and
removes the lines it fixes. "When" names the step that must handle it, or "any" for a cleanup PR.

| From | Minor | When |
|---|---|---|
| #30 (7a) | `infra/_modules/k3d` destroy ignores a failed `k3d cluster delete` | any |
| #30 (7a) | `docs/installation/sandbox.md` says `just dev` recreates the proxy; it is recreated only when the node address changes | any |
| #30 (7a) | `infra/_modules/argocd/bootstrap.sh`: the Forgejo check and its `yq \| kubectl` pipes run without pipefail | any |
| #30 (7a) | `just infra check` does not run `tofu validate` per module | any |
| #30 (7a) | `scripts/get-status`, `argocd-admin-password`, `onboard-user` default to production's kubeconfig | any |
| #31 (7b) | Argo CD's `argocd-notifications-cm` keeps the chart default `argocdUrl: https://argocd.example.com` | any |
| #31 (7b) | Forgejo's `[server]` ConfigMap duplicates what the chart derives from the route host | any |
| #31 (7b) | `global.timezone` is read by no chart | any |
| #31 (7b) | The tests hard-code `*.127-0-0-1.nip.io` | any (`testDomain` reads the environment where the testbed needs it) |
| #31 (7b) | woodpecker's and zot's config changes do not roll their pods (the woodpecker chart passes `podAnnotations` without `tpl`; Secrets ESO refreshes never would); 11b's rotations restart them explicitly, other changes still need a restart by hand | any (Reloader) |
| #31 (7b) | `scripts/check-env-config`'s untemplated-global check knows only four keys | any |
| #32 (7c) | `.direnv/` and `site/` are not git-ignored | any |
| #34 (8a) | `TestSecretsAudience` matches the plugin's "invalid audience" message | any |
| 8b | `infra/_modules/secrets/wait-namespace.sh` names OpenBao and `platform/openbao` (copied) instead of `infra-secrets` | any |
| 8b | `apply-secrets.sh`'s kubectl errors are hidden (OpenTofu suppresses output when `MANIFEST` is sensitive) | any |
| 8b | Losing `secrets/tfstate.json` on R2 (or the state passphrase) silently creates a new WireGuard key, which `Replace` pushes over OpenBao's (11a's backups hold volumes, not OpenTofu's state; 11b never rotates it) | 13 |
| 8b | Removing an `infra-secrets` entry leaves its Secret in the namespace; the docs' removal steps omit `kubectl delete secret` | any |
| 8b | `TestNtfyWatchdogScheduled` does not prove that heartbeats replace one scheduled message | any |
| 8b | `InfoInhibitor` reaches phones: the Alertmanager config override drops the chart's `null` receiver | any |
| 8b | `external-dns/cloudflare#zone_id` is pushed but never read (no `--zone-id-filter`) | any |
| 8b | The 8b spec lists hashicorp/random, which the `secrets` module does not use | any |
| 9 | `docs/installation/sandbox.md` still says the sandbox has no Loki; CLAUDE.md's "production-only allows … only rendered" bullet still lists Loki | any |
| 9 | Production's first Alloy start reads every current pod log from the start: Loki refuses lines older than 7 days and may answer 429 to the burst (default `ingestion_rate_mb` 4) | 12 (Nodes) |
| 9 (chat) | `system/rook-ceph` pins Rook 1.13.5 (latest v1.21.0): upgrade when Ceph first runs | 12 (Nodes) |
| mesh restart | `cni-watchdog.sh` logs "no running istio-cni pod" when a rollout briefly shows two istio-cni pods on a node (it skips that minute, which is safe) | any |
| mesh restart | `TestIstioCNIWatchdog` never exercises the BusyBox `date -D` fallback offline (the host has GNU date; `TestNodeRestart` covers it in the image) | any |
| mesh restart | The mesh-restart spec's RED bullet says the test "fails at its 12-minute limit" without saying the limit was 12 then | any |
| mesh watchdog | `cni-watchdog.sh` keeps restarting istio-cni, then istio-cni and ztunnel, every few minutes on a node stuck for another reason (a broken istio-cni after a bump), with no alert of its own: the pods' own alerts fire | any |
| mesh watchdog | A failed `kubectl delete` (one of the two pods already gone) ends the run under `set -e` and skips the later nodes: `--ignore-not-found` and a logged failure | any |
| mesh watchdog | A failed `kubectl get events` turns the second signal off without a log line | any |
| mesh watchdog | Events with only `eventTime` (no `lastTimestamp`, if the kubelet changes recorders) are skipped: fall back to `eventTime` without its fraction (BusyBox `date -D`) | any |
| mesh watchdog | `TestIstioCNIWatchdog` runs the script with the host's `sh` and GNU `date`, not BusyBox's | any |
| 14a | The test scopes spec's map table still lists `scripts/check-*`, `render-chart` and `cli/**` as Smoke only; `test/scopes.yaml` skips them | any |
| 14a | `lab test scope --check` covers only `components` globs: top-level files, `test/` non-test files, `metal/` and `cli/` are not checked (a new file there runs the whole suite) | any |
| 14a | `scope.SplitTerms` ignores escaped parentheses and an alternation sign inside a character class; no term uses them yet | any |
| dev shell cleanup | `metal/roles/pxe_server/templates/dnsmasq.conf.j2` uses `ansible.netcommon.ipmath`, which nixpkgs' Ansible does not ship (it moved to `ansible.utils`): `metal/` fails before and after the cleanup | 12 (replaces `metal/`) |
| 8e | `just dev-fresh` on the workstation hits the main run's 60-minute `go test` timeout (the first sync's waits count inside the tests) | any |
| 8e | Browser SSO tests flake on Kanidm's login page (OpenCloud, Grafana, paperless: "login page never loaded", `chrome-error`); a rerun passes | any |
| 10e | Woodpecker's server generates a temporary `WOODPECKER_GRPC_SECRET` at each start (agents sign in again with the agent secret; 11b's agent rotation restarts the server anyway) | any |
| 10e | `TestNodeRestart` once took about 23 minutes (more than its 20): the watchdog had to restart istio-cni three times; a rerun passed in 10 | 12 (nodes) |
| 11a | A whole-cluster restore takes the manual apps from their own pushes, another moment than OpenBao's: since 11b rotates generated secrets, a manual app restored from before a rotation holds the old value (rotate it again after such a restore) | any |
| 11b | `workload.WaitReady`: StatefulSets with `OnDelete` or a partitioned update never meet `CurrentRevision == UpdateRevision` (none in the repo), and a swallowed `Get` error leaves a timeout without its cause | any |
| 11b | ESO's `refreshTime` is in whole seconds: a refresh in the same second as the previous one is not seen (documented in `workload.go`) | any |
| 11b | The catalog's ExternalSecret reference check matches any `name:`/`forceRename:`/`existingSecret:` value, and does not check reader workload names (rendering every chart would be slow) | any |
| 11b | A lock renewal that reached the API server but whose answer was lost is now kept; one that times out on both the update and the re-read still aborts the run as "lost the lock" (fail-safe, resumable) | any |
| 11b | `lab secrets list` and the `--scheduled` plan write the first-seen baseline (a write from a read path) | any |
| 11b | `NtfyCheck` passes the token in `wget`'s arguments inside ntfy's own pod (where it is already in the environment) | any |
| 11b | `OpenTofu.Prepare` replaces again if it crashes between the apply and recording it (no overlap: harmless) | any |
| 11b | lab's image is pinned by hand after a release: a release that changes the catalog and the CronJob's access together runs the old catalog until the digest is set; the GHCR package must be public | 12 (production) |
| 11b | `TestRotation`'s sub-tests read the "old" value after an earlier interrupted run may already hold the new one (handled for `woodpecker.agent` only) | any |
| 11c | `just dev-clean` is not idempotent: a second destroy of an already-clean sandbox fails (`argocd` reads `cluster`'s outputs, which no longer exist), so `just dev-fresh` stops there; `just dev --full` does the same on an empty sandbox | any |
| 11c | The prune ClusterRole may delete any `VolumeSnapshot`, the hourly restore points included (RBAC cannot select by label; a ValidatingAdmissionPolicy limiting it to `velero.io/backup-name` would) | 15 (security) |
| 11c | A cut after Velero set a snapshot's content to `Retain` may leave the storage snapshot when prune deletes the `VolumeSnapshot` (both classes delete by default; unchecked on Ceph) | 12 (nodes) |
| 11c | The "being rotated by" error suggests `lab secrets force-unlock <name> <holder>` without `--env` (the holder check refuses a wrong cluster's lock) | any |
| 14d | `system/kiali` copies kiali-server 2.33.0's templates by hand: a Renovate bump of Kiali's image does not show changes the chart made to its config, RBAC or probes (compare with the chart's render when bumping) | any |
| 14d | No test that an anonymous visitor to Kiali is sent to Kanidm (the API without a session answers 401, checked by hand in the review) | any |
| 15a | The root `Dockerfile` (upstream's docs image) uses unpinned tags (`python:3.13-slim`, `nginx:1.27-alpine`); nothing here deploys it: pin or remove it | any |
| 15a | The blog's image in zot (`registry.<domain>/blog`) is private: `images` cannot pull it once its tag is pinned; give the check zot's read credentials or skip that registry | any |
| 15a | `.trivyignore.yaml`'s 90-day expiry for CVEs is a convention, not checked | any |
| 15a | `check-leaks` scans only the merge-commit range's own diffs: a change made only in a merge commit is not scanned; run directly without BASE it scans one commit | any |
| 15a | `TestCheckIaC` uses a fake Trivy: the ignore file's effect is proven only by the real run in CI | any |
| 15a | `TestBackupRestore`'s one-app restore read (again in 15b's run) the repository back as 404 right after `lab backup restore forgejo` (a rerun passed): find whether Forgejo answered before its restored data was in place | any |
| 14e | `deployedNamespaces` counts every namespace as present after a failed list without saying so: print one line | any |
| 14e | `Rotator.Run` still checks `overdue` for an entry skipped as not deployed (harmless while no optional app's entry has a period) | any |
| 14e | `just dev` counts a failed `git diff` (unknown base, shallow clone) as "changed" and deploys both optional apps silently: warn | any |
| 14e | `TestSetSandboxApps` does not assert the other documents unchanged or that a blank list is a no-op | any |
| 14e | `infra/_modules/argocd`'s hash covers `system/argocd` only: a change to `set-sandbox-apps.sh` (or `set-revision.sh`) does not re-run the bootstrap | any |
| 15b | `scripts/check-policy` takes about four minutes (every chart, both environments) on every pull request: limit it to the touched charts once policies are stable | any |
| 15b | `TestPolicy` judges only pods created a minute after the newest PolicyException (older reports keep the startup race's stale `fail`): on a fresh sandbox that is few pods; judge controller reports after a fresh scan before Enforce | 15b-2 |
| 15b | The Gateways (`gateway-istio`, `public-istio`) run without a seccomp profile (Istio generates their Deployments; excused): set it through the Gateways' `parametersRef` | 15b-2 |
| 15c | `AuditLogsMissing` fires only when every audit stream is silent: one server node whose Alloy or audit file stopped goes unseen (alert per `node`) | 12a |
| 15c | `OpenBaoConfigChanged` misses identity groups and entities, `sys/mounts` and a raft snapshot restore | any |
| 15c | `TestSecurityLogsQuietWhenIdle` runs only the critical rules on an idle sandbox: run the warnings too | any |
| 15c | `TestAuditPolicyRender` does not assert the level of a person's Secret or ConfigMap writes | any |
| 15c | `system/loki/values.yaml`'s sidecar comment says "no ruler here", stale since the ruler loads rules | any |
| 15c | Alloy reads only the current `audit.log`, not its rotated files: Alloy down while the log rotates loses the tail | any |
| sandbox-memory | `infra/_modules/k3d/main.tf`'s address pinning: a failure between disconnect and connect leaves that container off the k3d network (the cluster is recreated on the next run; a cache waits for k3d to connect it again) | any |
| sandbox-memory | `infra/_modules/k3d/main.tf` detects a dynamic address by Docker printing `<nil>` for `IPAMConfig`; another output would skip every container silently (test `.IPAMConfig.IPv4Address` instead) | any |
| sandbox-memory | `infra/_modules/k3d/main.tf` restarts the k3d containers to pin their addresses in GitHub Actions too, where nothing restarts them (tens of seconds) | any |
| restart fixes | A run cut by go test's 60m timeout skips `TestRotation`'s deferred cleanup: its Leases and interrupted entries (ntfy's phase `done`) stay, and the next scheduled run fails until `lab secrets force-unlock` and the entries are finished by hand | any |
| 15e | `.trivyignore.yaml` and `system/trivy-operator/values.yaml`'s ignore file are kept in step by hand: assert the same ids and dates in `TestTrivyOperatorRender` | any |
| 15e | `TestWorkflowsCheck`'s reason check accepts an empty `#` and does not look at `rules: <audit>: disable:` | any |
| 15e | `TestSupplyChain` proves the signed pod by 30 s without a `fail`: wait for its PolicyReport to exist first | any |
| 15e | The `homelab/trivy-config` hash comes from yq's `to_json(0)`: a yq bump can change it with no config change (the test prints the new value) | any |
| 15e | `verify-lab-image` sets no `credentials`: if ghcr's `homelab-lab` package is private, verification ends `error`; check it is public at the first signed release | 12b |
| 15e | The Security dashboard counts findings per workload: two workloads of one image in a namespace double its count | any |
| 15e | In the sandbox, Trivy cannot scan zot's images (sandbox CA) nor lab's image built locally: their scan Jobs fail and log errors | any |
| 15e | No check that each group test in `test/scopes.yaml` is also in `full` (SupplyChain and TrivyOperator were missing) | any |
| 12a-1 | The nodes resolve through `8.8.8.8`/`1.1.1.1` over UDP 53 (`profiles/network.nix`), outside `config.json`, and the home ISP's modem intercepts UDP 53: resolvers in `config.json`, or DoT | 12b |
| 12a-1 | `nixie` rewrites its hosts file with only the keys it knows when it records an install: a per-host `disk` in production's `hosts.json` is lost, so a disk other than `/dev/sda` needs another place | 12b |
| 12a-1 | On a fresh sandbox `ntfy-auth`'s ExternalSecret ran before `generated-secrets` pushed ntfy's values: ESO deleted the Secret (`SecretDeleted`) and waits for its 1h refresh, so ntfy stays in `CreateContainerConfigError` and `TestNtfy*` time out (a `force-sync` fixed it): a shorter refresh while the Secret is missing, or retry on missing keys | any |
| 12a-2 | `TestTestbedPostgresFailover` is only reachable on a machine that lists `platform/cloudnative-pg`: it passed once, at 2 vCPUs, and nothing runs it here | layer 3 |
| 12a-2 | SSH to a VM once took over 240 s after boot, reachable right after: `scripts/testbed-trust` rides it out, the cause is unknown | any |
| 12a-2 | `flake.lock` (pins `nixie`) is outside the testbed's gate: a bump every release would otherwise need a run | 12b |
| 12a-2 | `labEnv` returns `testbed`, which `env.Load` refuses: matters once the list names Forgejo | when it does |
| 12a-2 | `just infra deploy production --break-glass … --skip-proof …` ignores the skip without recording its reason | any |
| 12a-2 | `scripts/mark-proof` finds its repository with `gh repo view`, `verify` with `seed_repository`: a checkout whose `gh` default is another fork marks there | 16 (portability) |
| 12a-2 | Comments and docs that still say `values-dev.yaml` for what moved to `values-local.yaml` (`system/velero`, `system/kiali`, `set-backup-endpoint.sh`, `secrets-management.md`, `supply-chain.md`) | any |
| 12a-3 | `lab cluster upgrade`: a drain that fails leaves the node cordoned with its mon evicted, and the next run waits for a healthy cluster before draining, so it never starts until someone runs `kubectl uncordon` | any |
| 12a-3 | k3s stops itself once in a while right after a boot under load (a nil pointer in Flannel 0.28.4's `watchVXLANDevice`; `unable to initialize network policy controller: error getting node subnet`); systemd restarts it and the recovery goes on: check again with the next k3s | 12b |
| 12a-3 | `lab cluster shutdown`: when ssh fails for one node the others still go off, etcd loses quorum, and a rerun fails at its first `kubectl` saying the nodes are up and to run `lab cluster startup`, which cannot work either; the first error names the node, to power off by hand | 12b |
| 12a-3 | `cluster-startup` is `Type=oneshot` wanted by `multi-user.target`, so the target waits through `noout`'s 30 minutes; sshd and k3s are not behind it (`Type=exec` removes it; unchecked on a node) | any |
| 12a-3 | `TestPower*`'s "etcd whole" reads the node annotation `etcd.k3s.cattle.io/node-name`, which survives a cut: it only adds k3s being Ready; ask etcd's members | any |
| 12a-3 | `startupBegan` stays in the marker when `lab cluster startup` is interrupted and shutdown runs again, so the next boot gives `noout` no 30 minutes for a slow OSD | any |
| 12a-3 | Stale comments: `profiles/kube-vip.nix` ("ServiceLB serves the others"), `profiles/k3s-server.nix` ("HTTP (ServiceLB"), and `cluster-shutdown-rule.yaml` (a marker past two hours also means a failed shutdown left with the nodes up) | any |
| 12a-3 | Istio 1.31.2: a Gateway whose only pod's node goes keeps `Programmed=False` (`AddressNotAssigned`, no instances) once its pod is back, until istiod restarts (no event on the Gateway or Service clears it; same symptom as istio/istio#59501, closed): it serves, but Argo CD shows `istio-system` Progressing and `TestGatewayProgrammed`-like checks fail; measured once on the testbed | check on the next Istio bump |
| 15d | `.cluster-scan-accepted.yaml` keys an RBAC finding by its binding (the last three fields of kubescape's resourceID), not its subject: a ServiceAccount added to an accepted binding (`cluster-admin`) passes the scan; 15c's alert on cluster-admin bindings outside Argo CD catches that one. Key bindings by subject too | any |
| 15d | `TestScanBench` waits for exactly `Complete` in the Job's conditions: a failed Job (or one that lists another condition first) costs the full 5 minutes per node before `.status.succeeded` reports it | any |
| 15d | An accepted kubescape entry that no finding uses stays silently and could later accept a regression: report unused entries | any |
| 15d | kubescape's guard proves it saw resources (500, ztunnel), not that its controls ran: also require one known finding (C-0185 on `/ClusterRoleBinding/cluster-admin`) | any |
| 15d | `service-account-extend-token-expiration=false`: a client caching its projected token past an hour gets 401s; no test failed over the whole suite, but nobody read the audit log for ServiceAccount 401s over a long run | before production |
| 15d | `TestBackupRestore`'s "push asks first" failed once right after the sandbox started (`lab backup push`: "ntfy has no mounted volume"): `VolumeBytes` reads the kubelets' stats summary, which has no volume figures for a pod until its first collection; the rerun passed. Retry for a minute, or read the claims' pods instead | any |
| 15d | `just testbed` from stopped VMs failed once in `lab cluster enroll` ("Waiting for homelab-2 to join": the API server's `etcd failed` while the VMs were still booting, homelab-3 pending); the rerun passed. Enroll should retry an unhealthy API server while the nodes come up | 12b |
| 15f | `system/public-scan/files/scan.sh` drops nuclei's own `[FTL]`/`[ERR]` lines when nuclei fails: the log says only that it failed | any |
| 15f | The scan's last line `{"public_scan":"done","templates":N}` reports the last host's template count only | any |
| 15f | `docs/how-to-guides/public-surface.md` does not say how to import an existing `http_ratelimit` or `http_request_firewall_managed` entry point, if the first apply finds one | the first production apply |
| 15f | Gateway `public` enforces namespaces only; a route's hostnames are checked only in CI (`TestPublicRoutes`); one listener per host would enforce both | any |
| 15f | The scan's pod has requests but no memory limit | any |
| 14f-2 | `lab`/`labE`, `baoValueE`/`baoPut` (`sh -c`), `ssh`, `virsh` and `docker` set no `cmd.WaitDelay` nor a process group: a spec timeout kills `go run` but not its child (as before the move) | any |
| 14f-2 | `PowerCutPostgres`'s `SpecTimeout` (120m) equals `just test e2e`'s suite `--timeout 2h`: the suite's preempts it | any |
| 14f-2 | `lab test scope --check` accepts a group filter that selects only testbed or production specs (on the sandbox it runs only `smoke`) | any |
| 14f-2 | `TestE2EKeepsSelection` rebuilds each group's filter itself instead of feeding `lab test scope`'s main and alone output | any |
| 14f-2 | `PublicScan` and `MeshEnrolled` stop at their first finding where the old `assert`s reported all of them | any |
| 14f-2 | A global `Expect` inside an `Eventually` body (`gatewayAddress`, `kubectlJSON`, `woodpeckerRequest`, `bao`, …) fails at once instead of retrying a transient error (as `require` did) | any |
| 14g | `ArgoCDClusterSecret` checks every Application in `argocd`: one a failed run left behind (`forgejo-sync-probe`) fails it; narrow it to those the ApplicationSets own | any |
| 14g | No offline test proves server-side apply dropping a `homelab/app-*` label or the `revision` returning to the branch (the sandbox proved both by hand) | any |
| 14g | Forgejo's URL is in `infra/production/argocd/terragrunt.hcl` and `system/argocd-config/templates/forgejo-repository.yaml`, with no test that they agree | any |
| 14g | A production `config.json` without `seed_repository` fails as "REPOSITORY is required" (a precondition in `main.tf` would name it) | any |
| 14g | `backupS3Url` sits in the shared `&values`, so R2's endpoint reaches every Application's `system/egress` and `system/secret-store` sources, not only the component's | any |
| 14g | `cluster-secret.sh` splits `OPTIONAL_APPS` unquoted without `set -f` | any |
| 14g | `infra/_modules/argocd`'s `triggers_replace` takes `optional_apps` unsorted: the same apps in another order run the bootstrap again | any |
| 14g | `TrivyOperator`'s first wait (a report for excalidraw, 45 minutes) lacks the touch-once its later wait has: the operator dropping excalidraw at start failed `just dev-fresh` | any |
| 14g | The `SSO*` specs carry label `testbed`, whose layers run no Kanidm: `just test e2e --env testbed --labels argocd` (or `sso`) times out on them | any |
| 14f-2 | The `BeforeSuite` waits only for Argo CD to settle: after a sandbox restart Argo CD can report Healthy while the mesh is still deadlocked (the watchdog clears it in 5 to 14 minutes), so the specs through the mesh fail; it could also wait for ztunnel and every managed pod Ready, as `NodeRestart` does | any |
| 15g | Alertmanager groups by `namespace` only (`system/monitoring-system/values.yaml`): Falco's different rules in one namespace share a notification group, and events without a namespace join every other namespace-less alert; a route on `source="falco"` grouping by `alertname, rule, namespace`, like the audit route, would separate them | any |
| 15g | `SecretsGeneratedOnlyInOpenBao`'s `rollout status statefulset/woodpecker-agent` right after deleting the pod may return on the StatefulSet's old status, before the new agent is Ready; `kubectl wait --for=condition=Ready pod/woodpecker-agent-0` would not | any |
| 15g | `TestFalcoAccepted` renders only an `=` fixture: the real entries (an `in` with a nested list) are never rendered and checked, field, comp and value counts are not compared, `fields`/`values` are not `required`, and `accepted` in `values-dev.yaml`/`values-local.yaml` (which drops production's list) does not fail | any |
| 15g | The Buildah memfd exception could also require `proc.aname` `buildah`, narrowing it further | 15g-2 |
| 15g | Falco's DaemonSet has no checksum annotation for `falco-accepted-rules`: a change relies on Falco's `watch_config_files` reload (it reloaded live in the sandbox) | any |
| 15g | `TestFalcoRender` reads the chart's appVersion with `helm show chart --repo`, so the offline test needs the network | any |
| 15g | The `Falco` spec runs its probe at once: right after `just dev` syncs a change to the rules, Falco may reload them (kubelet updates the ConfigMap within about a minute) after the probe ran, so the spec fails once; it could wait for the rendered rules in the pod first | any |
| 15h | `woodpecker-localhost-seccomp` matches pod CREATE only: an ephemeral container (`pods/ephemeralcontainers`, `kubectl debug`) added to a running pod in `woodpecker` may name a `Localhost` profile unchecked; it needs the `pods/ephemeralcontainers` subresource in its rules, and only people who may update that subresource can try | any |
| 12b-1 | The testbed's VMs query `8.8.8.8`/`1.1.1.1` through NAT, which the ISP modem may intercept like production's (slow negative answers): give the testbed `network.dns` its libvirt gateway if its network serves DNS, and run `NodesResolveQuickly` there | any |
| 12b-1 | `lab cluster upgrade` on production ran only with nothing to change: its first real upgrade (drain, `nixos-rebuild`, Ceph healthy) on the hardware comes with 12b-2's first node change | 12b-2 |
| 12b-1 | `scripts/metal-nixie` counts `"machine_id_hash": ""` as recorded while nixie treats it as unrecorded, and a missing `jq` falls through to a confusing message instead of failing | any |
| 12b-1 | `ProductionVIPMoves` powers its node off before checking it can wake it (no workstation address in the nodes' /24 leaves the node off), and assumes a /24 rather than `config.json`'s `prefix_length` | any |
| 12b-1 | `just test e2e --env production --labels <broad filter>` (e.g. `production`, `!kiali`) still selects `hardware-proof`: only the empty filter excludes it | 12b-2 |
| 12b-1 | `metalEnv()` treats any `ENV` other than `testbed` as production, and the production recipe leaves `ENV` as the shell has it: export `ENV=production` and fail on anything else | any |
| 12b-1 | `TestProductionMetalUnit` and `TestProductionConfig` hard-code the machines' MACs, addresses and config values: replacing a machine means editing a test | any |
| 12b-2 | `EtcdLeaderChanges` overlaps kube-prometheus-stack's `etcdHighNumberOfLeaderChanges` and fires once per member: aggregate it (`max without (instance, pod)`) or keep only `EtcdSlowDisk` | any |
| 12b-2 | `ProductionPowerCutWithData` reads OpenBao once after the cut; wrap it in `Eventually` like the PostgreSQL read | 12b-2 |
| 12b-2 | `TestStageGatesApps` passes trivially if no `apps` cluster generator is rendered: assert one for production and dev | any |
| Matrix (#89) | The `Matrix` spec leaves a private room for `dev-admin` per run in the `daily` volume: leave and forget it in a `DeferCleanup` | any |
| Matrix (#89) | The `Matrix` spec finds tuwunel's "Continue sign-in" button by its text; a text or language change breaks it (its failure says where the browser stopped) | any |
| Matrix (#89) | tuwunel 2.0's `media.upload_min_free_space` (1 GiB) refuses uploads once about 4 GiB of `matrix-data`'s 5Gi is used: size it or note it | 12b-3 |
| Matrix (#89) | Matrix joins the sandbox's default apps (requests 272Mi, limits about 1.1Gi) unmeasured under the full suite, and `Falco`'s clean baseline has not run with it | any |
