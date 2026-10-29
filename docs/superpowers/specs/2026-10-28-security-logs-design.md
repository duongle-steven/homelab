# Step 15c: security logs

Status: designed in chat 2026-10-28. Branch: `feat/security-logs` from `dev`. Decisions marked *(user)* were agreed in
chat. The rest are *(Claude's call)*.

## Goal

Who did what to the cluster and to OpenBao is written down, kept, and searchable in Grafana. A few dangerous events
that no metric can show raise an alert.

## Why

*(user)* Most of the use is looking back: search the logs while debugging or checking what happened, the way one
searches Splunk. Alerts are only for events that are dangerous and invisible to metrics. The user did not pick the
alerts; they asked for what the community and enterprises usually do. They then accepted the list below as a start.

Today Loki holds pod logs and Kubernetes events. Nothing records API calls (who exec'd into a pod, who read a Secret,
who changed a role binding) or OpenBao requests (who read which secret, who logged in as admin).

## Decisions

1. **k3s's API audit log.** *(Claude's call, following GKE's audit policy and the Kubernetes documentation's example)*
   - One audit policy file in the repository, used by the sandbox (k3d) and by production's k3s config (today
     `metal/roles/k3s`; NixOS in step 12). Production is only rendered here, as with the rest of step 15.
   - Levels:
     - `None`: read-only noise. This covers watch/list/get by the node, controllers and the scheduler; leases;
       endpoints and EndpointSlices; events; `/healthz`, `/readyz`, `/livez`, `/metrics`, `/version`.
     - `Metadata` (who, when, verb, object, response code; never the body): Secrets, ConfigMaps, TokenReviews,
       SubjectAccessReviews, ServiceAccount tokens, `pods/exec`, `pods/attach`, `pods/portforward`.
     - `Request` (the body sent): writes to RBAC (Roles, ClusterRoles and their bindings), PolicyExceptions and
       Kyverno policies, admission webhook configurations, namespaces and CRDs.
     - `Metadata` for everything else. Stage `RequestReceived` is omitted.
   - **No secret value ever reaches Loki** *(anti-goal)*: a Secret's body is never logged, and neither is a
     ServiceAccount token request's response.
   - The API server writes a file on the node with size-based rotation (e.g. 100 MB, 3 files). The server's own
     rotation is enough; Loki is the long-term copy.
   - Alloy reads that file on server nodes and sends it with the label `job="audit/k3s"`.
2. **OpenBao's audit device.** *(Claude's call)*
   - A `file` device writing to `stdout`, declared in OpenBao's config. OpenBao 2.x forbids creating audit devices
     through the API unless told otherwise; check that config-declared devices work on 2.7.1, and fall back to an
     `initialize` stanza if they do not.
   - Its lines reach Loki through the pod-log path that already exists. Alloy labels them `job="audit/openbao"`, kept
     apart from the server's own log lines.
   - OpenBao HMACs secret values in audit entries by default; that stays on.
   - If every audit device fails, OpenBao refuses requests. stdout cannot fill up, so this is accepted.
3. **Retention.**
   - Audit streams are kept 90 days, other logs 30 days as today (the sandbox keeps 7 days for everything). This uses
     Loki's `retention_stream`. *(Claude's call)*
   - The sandbox measures the daily volume. If 90 days would not fit in production's Loki PVC (10Gi), the period is
     shortened, not the PVC raised: 60 days first, then 45 or 30 if 60 still does not fit *(user)*. The plan records
     the measurement and the period chosen.
4. **Search.** *(user: Splunk-like search)*
   - Grafana's Explore on the Loki data source.
   - A dashboard "Security" with the common questions: exec/attach/port-forward by user, Secret reads by
     non-service-accounts, RBAC changes, 401/403 by source, OpenBao logins and reads by role and path.
   - The how-to guide lists the LogQL queries.
5. **Alerts from logs.** *(user accepted this list as a start)*
   - Loki's ruler evaluates LogQL rules and sends to the existing Alertmanager, so alerts reach ntfy and Telegram like
     every other alert.
   - "A person" means a user that is not a ServiceAccount (`system:serviceaccount:…`) and not a node or control-plane
     component (`system:node:…`, `system:kube-…`, `system:apiserver`). The k3s admin (`system:admin`) counts as a
     person.
   - Expected actors are left out of every rule: Argo CD, ESO, the rotation CronJob, Velero, kaniop and Kyverno.

   | Alert | Source | Severity |
   |---|---|---|
   | A person exec'd, attached or port-forwarded into a pod | k3s | warning |
   | A person read a Secret | k3s | warning |
   | A binding to `cluster-admin` was created or changed, except by Argo CD | k3s | critical |
   | An anonymous request (`system:anonymous`) succeeded | k3s | critical |
   | Many 401/403 responses from one source in 5 minutes | k3s | warning |
   | A PolicyException was created or changed, except by Argo CD | k3s | warning |
   | A root token was used, or root token generation started | OpenBao | critical |
   | Many failed logins in 5 minutes | OpenBao | warning |
   | A policy, an auth method or an audit device changed outside the known flows | OpenBao | critical |
   | No audit line from k3s, or from OpenBao, for 15 minutes | Loki | critical |

   The last row matters most: an audit trail that stops silently is worse than none. OpenBao is quiet (ESO refreshes
   hourly, the raft snapshot runs hourly, health probes are not audited), so its window is 2 hours, not 15 minutes;
   k3s's stays 15 minutes *(Claude's call: a longer window rather than a heartbeat component)*.

   "Known flows" in OpenBao *(Claude's call)*: the roles `rotator`, `writer` and `infra` and the stores' read roles
   are expected, and only on the paths their policies allow. A person using the `admin` role is not excluded: changing
   a running OpenBao is rare and deliberate, and an alert for one's own change is accepted, like a sudo notice.

   Tools that use the admin kubeconfig (the sandbox tests, `lab`, OpenTofu) also exec into pods and read Secrets as
   a person, so they raise the first two alerts. This is accepted *(user)*: the alert names the user, and Alertmanager
   groups these two alerts by user so one run sends one notification, not one per call *(Claude's call)*. No separate
   identity for tooling in this step.

   Thresholds ("many") are the builder's to set from the sandbox's normal traffic. Each rule carries a `summary` that
   names the user, the object and the query to open in Grafana.
6. **Tests.** *(Claude's call)* A sandbox test, `TestSecurityLogs`:
   - `kubectl exec` into a pod. A matching `audit/k3s` line with that user appears in Loki, and the exec alert reaches
     Alertmanager.
   - Read a secret in OpenBao. A matching `audit/openbao` line appears, and the secret's value is not in it.
   - Loki holds no line containing a known generated secret value, from either source.
   - Each rule's LogQL is checked offline: it parses, and the ruler's ConfigMap renders.

   The scopes map gets an entry for the new paths.
7. **Mesh and egress.** Loki's ruler calls Alertmanager inside the cluster, so `monitoring-system`'s `allow` admits
   Loki. Nothing calls the internet.
8. **Docs.**
   - `docs/how-to-guides/security-logs.md`: where the logs are, the queries, what each alert means and what to do.
   - CLAUDE.md: 15c done, the Sandbox section's Loki line, and the "before production" items (the audit policy on the
     real nodes, the audit volume against the 10Gi PVC).

## Not in this step

- Runtime detection (Falco, Tetragon): step 15g.
- Audit logs of Cloudflare, GitHub and Kanidm: listed under Ideas.
- Restricting who may create a `PolicyException`: step 15b-2. This step only alerts on it.
- Shipping logs off the cluster. Loki is in-cluster: an attacker with cluster-admin can delete what Loki holds.
  Accepted for a homelab; noted in the guide.

## Left to the builder

- How Loki loads the rules (the ruler's local storage from a ConfigMap) and the exact Alertmanager URL.
- Where the audit policy file lives and how k3d mounts it into the node.
- How Alloy tells OpenBao's audit lines from its server log lines (JSON with `type` `request`/`response`).
- The thresholds, after watching the sandbox's normal traffic.
