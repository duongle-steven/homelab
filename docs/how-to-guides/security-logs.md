# Security logs

Who did what to the cluster and to OpenBao is in Loki, beside the other logs ([Logs](logs.md)): Grafana → Explore →
data source Loki, or the dashboard **Security**. A few dangerous events raise alerts, which reach ntfy and Telegram like
every other alert ([Alerts](alerts.md)).

| Stream | What | Kept |
|---|---|---|
| `{job="audit/k3s"}` | the API server's audit log: one JSON event per request (who, verb, object, response code) | 90 days (7 in the sandbox) |
| `{job="audit/openbao"}` | OpenBao's audit device: every request and response (who, path, operation); secret values are HMACed | 90 days (7 in the sandbox) |

## Where they come from

- **k3s**: the API server writes `/var/log/kubernetes/audit/audit.log` on each server node (100 MB, 3 rotated files)
  under one policy, `metal/roles/k3s/files/audit-policy.yaml`, used by production's nodes and by the sandbox's k3d
  node. Read-only noise from the control plane, leases, events and health checks is not logged. Secrets, ConfigMaps,
  token and access reviews, exec, attach and port-forward are logged at `Metadata` (never a body). Changes to RBAC,
  Kyverno's policies and exceptions, admission webhooks, namespaces and CRDs carry the body sent. Everything else
  is `Metadata`. Alloy reads the file on the node.
- **OpenBao**: a `file` audit device writing to stdout, declared in its config (`platform/openbao`), so OpenBao
  creates it at every start. Alloy labels these lines `audit/openbao` and leaves OpenBao's server log under
  `job="pods"`. If every audit device fails OpenBao refuses requests; stdout does not fill up.

!!! warning "Command lines are logged"
    An exec's command is part of its request URL, which the audit log records. Pass a secret on stdin
    (`bao kv put secret/x key=-`), never as an argument.

Loki runs in the cluster: someone with cluster-admin can delete what it holds. Shipping the logs off the cluster is not
done (accepted for a homelab).

## Queries

The JSON fields come out with `| json` (`user_username`, `verb`, `objectRef_resource`, `objectRef_namespace`,
`objectRef_name`, `responseStatus_code`, `sourceIPs`; OpenBao: `type`, `auth_display_name`, `auth_policies`,
`request_path`, `request_operation`, `request_remote_address`, `error`).

| Query | Shows |
|---|---|
| `{job="audit/k3s"} \| json \| objectRef_subresource=~"exec\|attach\|portforward"` | who opened a shell, attached or port-forwarded, into which pod |
| `{job="audit/k3s"} \| json \| objectRef_resource="secrets" \| user_username!~"system:serviceaccount:.*"` | Secret reads by people |
| `{job="audit/k3s"} \| json \| objectRef_resource=~".*rolebindings\|.*roles" \| verb!~"get\|list\|watch"` | RBAC changes, with the body |
| `{job="audit/k3s"} \| json \| responseStatus_code=~"401\|403"` | refused requests |
| `{job="audit/k3s"} \| json \| user_username="system:admin"` | everything done with the admin kubeconfig |
| `{job="audit/openbao"} \| json \| type="request" \| auth_policies=~".*admin.*"` | OpenBao requests with the admin role |
| `{job="audit/openbao"} \| json \| request_path=~"auth/.+/login" \| error!=""` | failed OpenBao logins |
| `{job="audit/openbao"} \|= "secret/data/forgejo/"` | who read Forgejo's secrets |

## Alerts

Loki's ruler evaluates `system/loki/files/audit-rules.yaml` every minute and sends to Alertmanager. "A person" is any
user but service accounts, nodes and the control plane; `system:admin`, the admin kubeconfig, is a person. Tools using
that kubeconfig (the sandbox's tests, `lab`, OpenTofu) exec and read Secrets too, so they raise the first two alerts:
this is accepted, and Alertmanager groups the audit alerts by alert and user, so one run notifies once.

| Alert | Severity | Meaning | What to do |
|---|---|---|---|
| `AuditExecByPerson` | warning | a person exec'd, attached or port-forwarded into a pod | expected if it was you or your tool; otherwise find the user and the session, revoke the credential |
| `AuditSecretReadByPerson` | warning | a person read a Secret | same |
| `AuditClusterAdminBinding` | critical | a binding to `cluster-admin` was created or changed, not by Argo CD | read the body (`requestObject`); delete the binding unless it is in git |
| `AuditAnonymousAllowed` | critical | an unauthenticated request succeeded (outside the public OIDC discovery URLs) | check the URL; anonymous access should only reach health and discovery |
| `AuditManyDenied` | warning | more than 20 requests from one address refused in 5 minutes | a broken client or someone probing: find the address and the user |
| `AuditPolicyExceptionChanged` | warning | a `PolicyException` changed outside Argo CD | an exception bypasses policy; revert it unless it is in git |
| `OpenBaoRootToken` | critical | a root token was used, or root token generation started | nothing uses root tokens: find who and why, revoke it |
| `OpenBaoManyFailedLogins` | warning | more than 10 failed logins at one auth path in 5 minutes | a misconfigured role or someone guessing |
| `OpenBaoConfigChanged` | critical | a policy, an auth method or role, or an audit device changed | expected only when a person changes OpenBao with the admin role (`docs/concepts/secrets-management.md`) |
| `AuditLogsMissing` | critical | no line from k3s for 15 minutes, or from OpenBao for 2 hours | an audit trail stopped: check Alloy on the server nodes, the audit flags, `bao audit list` |

## Retention

Audit streams are kept 90 days (`retention_stream` in `system/loki/values.yaml`), other logs 30. If they outgrow
Loki's 10Gi volume, the period is shortened (60 days, then 45 or 30), not the volume raised.
