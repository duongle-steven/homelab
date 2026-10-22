# Deploy to Forgejo

GitHub is where work happens (pull requests, `checks`, the sandbox). Production's Argo CD syncs `ops/homelab` on
Forgejo, which changes only when a commit is deployed:

```sh
make infra env=production       # the infrastructure, Forgejo's orgs ops and ci, the push, the Argo CD bootstrap
cd cli && go run ./cmd/lab infra forgejo push   # only the push
```

Both refuse anything but `origin/master` checked out without changes (and no untracked file under `infra/` or
`system/argocd/`) whose check `checks` passed and whose merged pull request's check `sandbox` passed
(`lab infra forgejo verify`). Nothing force-pushes: roll back with a revert through a pull request.

## What `lab infra forgejo sync` owns

`sync` (part of `make infra env=production`) owns orgs `ops` and `ci` and Woodpecker's OAuth client; nothing
declares other orgs, teams or repositories. Running it again keeps what is right, fixes what is wrong and creates
what is missing:

- orgs `ops` and `ci`, private (made private again if someone changed that);
- `ops/homelab`, private, default branch `master`, created empty: the first deploy fills it;
- local users `deploy` (write) and `argocd` (read), restricted to the repositories they collaborate on; `sync`
  refuses a user of that name who signed in with Kanidm, and a team granting either of them more;
- `master`'s protection: only `deploy` may push, nobody force-pushes or deletes it, Forgejo's site admins included
  (they can still edit the protection, which the next `sync` restores);
- their tokens in OpenBao: `secret/forgejo/deploy` (the push) and `secret/argocd/forgejo` (Argo CD's read-only
  credential, `argocd-repo-forgejo`);
- in org `ci`, its team `developers` (write on code and pull requests, may create repositories in `ci`,
  not all of them): Woodpecker admits only `ci`'s members, and Forgejo's Kanidm login source makes `homelab-admins`
  its owners and `homelab-editors` its developers ([Woodpecker](woodpecker.md)); no team of `ci` reaches `ops`;
- the OAuth2 application `woodpecker` (confidential, redirect `https://ci.<domain>/authorize`) and its credentials in
  `secret/woodpecker/forgejo` (`client_id`, `client_secret`, `redirect_uri`); a changed application is updated,
  which issues a new secret: restart Woodpecker's server afterwards
  (`kubectl rollout restart statefulset/woodpecker-server -n woodpecker`).

Other repositories (for example the blog) live in org `ci`, created there or mirrored, not through `push`.

## Break-glass: GitHub or the internet is down

Production keeps running without GitHub: Argo CD reads Forgejo. A fix that cannot wait for GitHub skips `verify`'s
GitHub checks, and nothing else:

```sh
direnv exec . scripts/ci-check all             # the checks GitHub would have run, locally
git switch -c fix/<what> && git commit ...     # a branch: master stays GitHub's
make infra env=production break_glass="<why>"  # verify without GitHub, the infrastructure, sync, the push, the bootstrap
```

Before anything is applied, `verify --break-glass` still refuses uncommitted changes, untracked files under `infra/`
or `system/argocd/` and a commit that does not descend from what `ops/homelab`'s `master` holds (so production never
runs infrastructure from a commit the push would then refuse); the push prints the reason. `break_glass` cannot be combined
with `force=1`. When GitHub is back, push the branch and open a pull request into `dev` that says why, then the
release pull request into `master`: merge commits only (never squash or rebase), so `master` contains the deployed
commit and the next normal deploy fast-forwards.

What still depends on the internet:

- OpenTofu's state is on R2: with no internet `make infra` cannot run. Push alone:
  `cd cli && go run ./cmd/lab infra forgejo push --break-glass="<why>"`.
- Charts served from `*.github.io` (app-template, Argo CD's) and images on `ghcr.io` are GitHub too: Argo CD renders
  only charts its repo server still caches, and a node runs only images it already pulled.
- Kanidm and Forgejo work on the LAN; Cloudflare's Tunnel (the public routes) does not.

The last resort, when nothing can be pushed: pause the Application's auto-sync in Argo CD (an admin, in its UI),
change the resource with `kubectl`, then commit the same change and resume auto-sync once it is deployed; Argo CD
would revert a manual change while auto-sync runs.
