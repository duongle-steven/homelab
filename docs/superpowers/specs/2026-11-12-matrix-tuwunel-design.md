# Matrix on tuwunel

Status: designed in chat 2026-11-12. Decisions marked *(user)* were made in chat; *(Claude's call)* marks the builder's
own, open to review. A step of its own between 12b-2 and 12b-3: Matrix is an app, so production deploys it only once
the stage is `all` (`docs/superpowers/specs/2026-11-12-production-platform-design.md`). It must reach `master` before
that switch, or production deploys the old Dendrite first.

## Intent

Replace the only app chart, `apps/matrix/` (Dendrite and Element Web from two third-party charts, the domain written in
its values because neither chart templates it), with one app-template file like every other app. Upstream did the same
(checked 2026-11-12): tuwunel and FluffyChat in `apps/matrix.yaml`. Dendrite is in maintenance mode.

What Matrix is for is not decided yet *(user: a homelab to learn the apps)*, so the defaults are the safe ones, and each
is one setting to change later.

## Goals

- **`apps/matrix.yaml` (app-template)** *(Claude's call, as upstream)*:
  - the server: tuwunel (`ghcr.io/matrix-construct/tuwunel`, the latest stable pinned) at `matrix.<domain>`;
  - the web client: FluffyChat (`ghcr.io/krille-chan/fluffychat`) at `chat.<domain>`;
  - the domain from `global.baseDomain`;
  - `server_name` `matrix.<domain>`, with `.well-known` for clients.
- **Sign-in through Kanidm only** *(Claude's call)*: tuwunel's identity provider with a `KanidmOAuth2Client` `matrix` in
  namespace `matrix` (PKCE). No password login, no open registration.
- **Federation off** *(Claude's call)*: `allow_federation = false` and no route for it, so only homelab accounts chat. The
  egress entry for 8448 goes.
- **In the sandbox too:** in `values-dev.yaml`'s apps list, with a spec proving an account signs in through Kanidm and
  sends a message. The old chart never ran there.
- **Backups:** the data volume `daily`, as Matrix's was.
- **The old chart removed:** `apps/matrix/`, its entries in `scripts/check-backup-groups`, `test/scopes.yaml` and the
  docs. `CLAUDE.md`'s Portability line about Matrix's domain goes too.

## Non-goals

- Federation, bridges (Telegram, Messenger), voice and video calls (TURN): one setting or one step each, once wanted.
- Moving data: production never ran the old Matrix.

## Anti-goals

- **Production deploying Dendrite before this reaches `master`.** The stage stays `platform` until then.
- **A public route that exposes more than the client and the client API.** `chat.` stays public, as today;
  `matrix.` (the client API) is public only as far as the Tunnel's allow-list says (15f).

## Left to the builder

- tuwunel's cache and memory settings, the client's `config.json`, the resource limits. Upstream's are a starting point.
- How the spec drives FluffyChat's or the API's SSO flow: the API (`/_matrix/client/v3/login` with `m.login.sso`) through
  Kanidm's form, as the other SSO specs do, is enough.
- Whether `matrix.` joins Gateway `public`'s namespaces (15f's map): phones on a mobile network need the client API, so
  yes unless the person keeps Matrix LAN-only.
