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

    Add the person to one of the three role groups (`admins`, `editors` or `users`), and optionally to the access groups `budget`, `documents` and `files`:

    ```sh
    ./scripts/onboard-user johndoe "John Doe" "johndoe@example.com" users budget files
    ```

    The script prints the `idm_admin` password for the login prompt and ends with a credential reset link.
    Let the user scan the QR code or follow the link to set up a passkey or password + TOTP (Kanidm requires MFA).

    The role group decides access to the admin tools: `admins` administer Grafana, Argo CD and the registry; `editors` edit Grafana dashboards, sync Argo CD apps and push images; `users` view and pull. In Forgejo, every role group gets an account on first login; `admins` are site admins.

    Apps with personal data only admit their access group, whatever the role group:

    | Kanidm group | App | Access |
    |---|---|---|
    | `homelab-budget` | Actual Budget | an account on first login (the first one becomes the server owner) |
    | `homelab-documents` | paperless | an account on first login with the permissions of the paperless group `homelab-documents` (documents, tags, mail rules…, created at every start; groups set in the paperless UI are replaced at each login); members of `homelab-admins` also become superusers |
    | `homelab-files` | OpenCloud | an account on first login (Kanidm asks once to consent); members of `homelab-admins` get the Admin role, the others User; the role follows the groups at each login |

    To grant access to an existing person later: `kanidm group add-members homelab-budget johndoe --name idm_admin`.

    To offboard someone, remove them from their groups (or delete the person) in Kanidm **and** delete their API keys in the registry: zot keeps the groups from their last login for existing keys. Also disable the user in Forgejo: their access tokens and SSH keys keep working after Kanidm removes the person. OpenCloud keeps the files of removed people; delete their account in OpenCloud's user management.
