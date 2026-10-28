# Step 14e: optional apps in the sandbox

Status: designed in chat 2026-10-28. Branch: `feat/sandbox-optional-apps` from `dev`. Decisions marked *(user)* were
made in chat; the rest are *(Claude's call)*.

## Why

The sandbox shares 12 GB (WSL) with everything else. Since 14d, 15a and 15b (Kiali, Istio's metrics, Kyverno), its
node used 8.9 of 11.7 GB under the suite. k3s twice lost its own leader lease and restarted mid-run (2026-10-27/28), so
`TestBackupRestore` and the SSO tests failed for want of memory, not for a bug.

Paperless and OpenCloud take about 900 MB together. They are apps, they were tested many times, and most changes never
touch them.

## Decisions

1. *(user)* **Paperless and OpenCloud are optional in the local sandbox.** They are deployed only when wanted:
   - **automatically**, when the branch changes their app file (`apps/paperless.yaml`, `apps/opencloud.yaml`): an
     upgrade or a large change is tested before production;
   - **on request**, with `make dev apps=paperless,opencloud` (or `apps=all`), e.g. with `make dev-fresh`.
2. *(Claude's call)* **The release sandbox keeps everything.** GitHub Actions' runner has more memory: `sandbox-release`
   and `sandbox-retarget` (dev → master) run with `apps=all`, so nothing reaches production untested.
3. *(user)* **Prometheus is slimmer in the sandbox:** one day of retention (`values-dev.yaml`). It is the largest
   component there (1.5 GB), and the tests read only recent series.
4. *(Claude's call)* **How.**
   - `system/argocd/values-dev.yaml` lists only the always-on apps.
   - `infra/sandbox/argocd` reads `HOMELAB_SANDBOX_APPS` (space-separated names) into the module's `optional_apps`.
     `infra/_modules/argocd/set-sandbox-apps.sh`, beside `set-revision.sh`, adds `apps/<name>.yaml` to the `apps`
     ApplicationSet's git generator. The bootstrap re-applies when the list changes.
   - `make dev` computes the list: the `optional_apps` named in the Makefile whose file changed since the base, plus
     `apps=`. It prints the list.
   - Removing an app from the list deletes its Application. Argo CD keeps a namespace it created
     (`CreateNamespace=true`), so `make dev` deletes the namespaces of the optional apps it left out, volumes
     included; that is fine in the sandbox. *(review)*
   - The tests get the list (`HOMELAB_SANDBOX_APPS`): an app it names that is absent fails, never skips, so the
     release sandbox cannot go green without them. *(review)*
5. *(Claude's call)* **Tests follow what is deployed.**
   - `TestSSOPaperless`, `TestSSOOpenCloud` and the smoke list skip an app whose Application does not exist, and say so.
   - `lab secrets rotate --all --env sandbox` skips an entry whose readers' namespaces do not exist, reported as
     "not deployed", instead of failing. Production is unchanged: a missing namespace there is hit, not skipped.
   - The scheduled rotation already rotates no paperless entry (they have no period).
6. *(Claude's call)* **Docs:** `docs/installation/sandbox.md` and `docs/how-to-guides/sandbox-tests.md` (optional apps,
   `apps=`), and CLAUDE.md's Sandbox section.

## Not in this step

- Making other components optional, and limits on other pods.
- Production: unchanged.
