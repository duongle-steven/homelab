# User onboarding

=== "For user"

    ## Create user

    Ask an admin to create your account, provide the following information:

    - [ ] Full name (John Doe)
    - [ ] Select a username (`johndoe`)
    - [ ] Email address (`johndoe@example.com`)

    ## Install companion apps

    For all users:

    - [ ] A password manager (I personally recommend [Bitwarden](https://bitwarden.com/download))
    - [ ] A [Matrix chat client](https://matrix.org/clients) (optional, you can use the web version)

    For technical users:

    - [ ] [Docker](https://docs.docker.com/engine/install)
    - [ ] [Nix](https://nixos.org/download) and [direnv](https://direnv.net) (optional, but highly recommended)
    - [ ] [Lens](https://k8slens.dev) (optional, you can use the included `kubectl` or `k9s` command in the tools container)

=== "For admin"

    Add the person to one of the three groups (`admins`, `editors` or `users`):

    ```sh
    ./scripts/onboard-user johndoe "John Doe" "johndoe@example.com" users
    ```

    The script prints the `idm_admin` password for the login prompt and ends with a credential reset link.
    Let the user scan the QR code or follow the link to set up a passkey or password + TOTP (Kanidm requires MFA).

    The group decides access everywhere: `admins` administer Grafana, Argo CD and the registry; `editors` edit Grafana dashboards, sync Argo CD apps and push images; `users` view and pull.

    To offboard someone, remove them from their group (or delete the person) in Kanidm **and** delete their API keys in the registry: zot keeps the groups from their last login for existing keys.
