# Deploy to Forgejo

GitHub is where work happens (pull requests, `checks`, the sandbox). Production's Argo CD syncs `ops/homelab` on
Forgejo, which changes only when a commit is deployed:

```sh
make infra env=production       # the infrastructure, Forgejo's org ops, the push, the Argo CD bootstrap
cd cli && go run ./cmd/lab infra forgejo push   # only the push
```

Both refuse anything but `origin/master` checked out without changes (and no untracked file under `infra/` or
`system/argocd/`) whose check `checks` passed and whose merged pull request's check `sandbox` passed
(`lab infra forgejo verify`). Nothing force-pushes: roll back with a revert through a pull request.

## What `lab infra forgejo sync` owns

`sync` (part of `make infra env=production`) owns org `ops` and nothing else in Forgejo; nothing declares other
orgs, teams or repositories. Running it again keeps what is right, fixes what is wrong and creates what is missing:

- `ops/homelab`, private, default branch `master`, created empty: the first deploy fills it;
- local users `deploy` (write) and `argocd` (read), restricted to the repositories they collaborate on; `sync`
  refuses a user of that name who signed in with Kanidm, and a team granting either of them more;
- `master`'s protection: only `deploy` may push, nobody force-pushes or deletes it, Forgejo's site admins included
  (they can still edit the protection, which the next `sync` restores);
- their tokens in OpenBao: `secret/forgejo/deploy` (the push) and `secret/argocd/forgejo` (Argo CD's read-only
  credential, `argocd-repo-forgejo`).

Other repositories (for example the blog for Woodpecker, step 10e) reach Forgejo by a mirror or by hand.
