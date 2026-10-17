# Post-installation

## Backup secrets

Save the following files to a safe location like a password manager (if you're using the sandbox, you can skip this step):

- `~/.ssh/id_ed25519`
- `~/.ssh/id_ed25519.pub`
- `./infra/production/kubeconfig.yaml`
- `~/.config/homelab/cloudflare.env`
- `./.state/production/` (OpenTofu's state: OpenBao's unseal key, the Cloudflare tokens, the WireGuard key; until
  step 8c moves it to R2)

## Admin credentials

- ArgoCD:
    - Username: `admin`
    - Password: run `./scripts/argocd-admin-password`
- Forgejo (people log in with Kanidm; `forgejo_admin` works only while the Forgejo pod keeps running: it does not start while Kanidm is unreachable, so bring Kanidm back first after an outage):
    - Username: `forgejo_admin`
    - Password: get from `global-secrets` namespace
- Kanidm:
    - Usernames: `admin` and `idm_admin`
    - Password: kept by the kaniop operator, `kubectl -n kanidm get secret kanidm-admin-passwords -o jsonpath='{.data.IDM_ADMIN_PASSWORD}' | base64 -d` (`ADMIN_PASSWORD` for `admin`)
- Jellyfin and other applications in the \*arr stack: see the [dedicated guide for media management](../how-to-guides/media-management.md)
- zot: people log in with Kanidm; the `ci` account (htpasswd, password in `global-secrets` `registry.ci`) is for automation
- Other apps:
    - Username: `admin`
    - Password: get from `global-secrets` namespace

## Backup

Now is a good time to set up backups for your homelab.
Follow the [backup and restore guide](../how-to-guides/backup-and-restore.md) to get started.

## Run the full test suite

After the homelab has been stabilized, you can run the full test suite to ensure that everything is working properly:

```sh
make test
```

!!! info

    The "full" test suit is still in its early stages, so any contribution is greatly appreciated.
