# The blog on the app road

Status: designed in chat 2026-11-21. *(user)* marks what was decided in chat; *(Claude's call)* marks the builder's
own choices, open to review. It follows app delivery (`docs/superpowers/specs/2026-11-18-app-delivery-design.md`,
merged as #109), whose road the blog is the first real app to take.

## Intent

The homelab's public face: a personal page, CV-like at first, with blog posts later *(user)*. No concrete content yet,
so this step builds everything around the content. That means the source repository, the build, the road to
production and the public address. Writing the page is then only editing Markdown in a repository. The blog is also
the first real app on the app road: every content change is a new image that the CI builds and signs, Kargo verifies
and promotes, and Argo Rollouts releases *(user, 2026-11-18: the blog promotes itself)*.

## Decisions made in chat

- **Source** in Forgejo's org `ci`, built by the golden path (platform/ci) *(user)*.
- **Promotion** automatic, no person approves (the teaching app will) *(user)*.
- **Address**: `www.hoangduong.dev`, with `hoangduong.dev` (the apex) redirecting there permanently. The redirect is a
  Cloudflare Redirect Rule written in OpenTofu, unlike upstream's hand-made one *(user, 2026-11-21, after weighing the
  apex: one canonical name with a 301 from the other is the only firm practice; `www` needs no apex listener, no apex
  name on the wildcard certificate and no special case in the public map, and an apex CNAME is not portable off
  Cloudflare)*.
- **Generator**: Hugo with the PaperMod theme. PaperMod is a blog theme with a profile mode, so the home page
  introduces its owner and posts sit below *(user)*. Content is Markdown: Hugo's native format, easy to write and to edit
  in Forgejo's web editor, with shortcodes for what it lacks *(user, 2026-11-21)*. That it also keeps the engine
  swappable (even for one written by hand, in Rust) is a bonus, not the reason.
- **Serving**: the image holds only the built site and a small web server that runs as non-root, with two replicas
  *(user)*. The server is `static-web-server` (Rust, a few MB, non-root by default, made for static files)
  *(Claude's call, agreed 2026-11-21)*.
- **Release: blue-green, not a canary** *(user, 2026-11-21)*. PaperMod names its stylesheet by a hash of its content
  and checks it (integrity). A canary splits traffic request by request, so a page from one version would ask for an
  asset only the other version has, and load unstyled. `gateway-success-rate` counts only 5xx, and `canary-responds`
  checks only the main page, so neither would catch it. Blue-green serves one version at a time:
  - the new one comes up behind a `preview` Service and is checked there before the switch;
  - the switch moves every visitor at once;
  - the old one stays up for a while, so a check failing after the switch sends everyone back at once.

  Apps with a server of their own (the teaching app) keep the canary: the road supports both.
- **Before real content**: `www` shows a minimal "coming soon" page *(user, 2026-11-21)*. `ci/blog` is created from
  the template only when its owner has real content, so the template's placeholder is never public.

## Goals

1. **A Backstage template for a Hugo site** (`backstage/templates/hugo-site`), beside `ci-repository`. It creates a
   repository in org `ci` holding:
   - a PaperMod site in profile mode, with placeholder text and no real person's data;
   - a sample post marked as a draft;
   - a `Containerfile`: Hugo builds the site in a first stage, and the final stage is `static-web-server` serving it
     on 8080 as non-root;
   - `catalog-info.yaml` and a README on writing and previewing (`hugo server`).

   Every version in it is pinned exactly (Hugo, the theme's commit, `static-web-server`'s image), and the theme comes
   checked against a known hash. The golden path builds it as it builds any repository.
2. **`apps/blog.yaml` on the road**, as `test/fixtures/apps/delivery-test.yaml` shows and the guide's "Adding an app to
   the road" lists:
   - the image is `registry.<domain>/blog`, pulled as `puller` (`zot-pull`), with a base tag to run before its first
     promotion;
   - two replicas behind a `Rollout` with `strategy.blueGreen`:
     - an `active` and a `preview` Service on 8080, and its `HTTPRoute` on the public Gateway for `www.<domain>`,
       proxied, naming only `active`;
     - automatic promotion;
     - a pre-promotion analysis on `preview` that fetches the page and every stylesheet, script and image it names,
       failing on anything but a 2xx;
     - a post-promotion analysis with `gateway-success-rate` on `active`, whose failure switches back to the previous
       version, kept up meanwhile (`scaleDownDelaySeconds`);
     - `progressDeadlineAbort`.

     The pre-promotion check is a new metric or template beside `canary-responds` (`system/argo-rollouts`).
     `RolloutAborted` covers blue-green aborts too: check its phases.
   - the "coming soon" page as its state before the first promotion (see Left to the builder);
   - Kargo's ProjectConfig (both stages promote themselves), Warehouse (commit tags only) and Stages `verify` and
     `production`;
   - the ServiceAccount `ci-signature`;
   - no PolicyException: the image is non-root, so Pod Security restricted holds.

   The lists the guide names gain `blog` where they lack it: Kargo's `projects`, `zotPull`, the destinations of
   `registry.puller`, `rotationReaders`, `publicKeyReaders`, the rotation catalog and OpenBao's policy `rotator`.
   `deliveredApps`, the templatePatch and the `releases` source already have it.
3. **The apex redirect** in `infra/_modules/cloudflare`, production only. The zone holds no record today: the
   owner added none when registering, so the homelab's code controls it all *(user)*. The plan still starts with a
   read-only look at the zone before the first apply. It covers:
   - a proxied record for the apex, so Cloudflare answers for it;
   - a Redirect Rule sending `https://<domain>/<path>?<query>` to `https://www.<domain>/<path>?<query>` with a 301.

   It is checked by the module's `tofu test`. The Free plan's single rate-limit rule stays untouched; redirects are
   their own phase.
4. **Proven in the sandbox**: a new spec, `Blog` (label `blog`), runs in a new sandbox group `blog` (`apps/blog.yaml`;
   it needs `delivery`). The spec:
   - creates the repository from the Hugo template through Backstage, as a person would;
   - lets the CI build and sign it, Kargo promote it and the blue-green release switch to it;
   - checks that `www.<sandbox domain>` serves the placeholder profile through the Gateway.

   It removes the repository afterwards, as AppDelivery does.
5. **Documentation**: a guide `docs/how-to-guides/blog.md` covering:
   - creating `ci/blog` from the template once, in production, when the content is ready (until then `www` shows the
     "coming soon" page);
   - writing and previewing a page or a post;
   - what happens on a push (the road);
   - the address and its redirect.

   Also updated: `docs/how-to-guides/public-surface.md` (the redirect), `app-delivery.md` (the blog is on the road)
   and CLAUDE.md (the roadmap and the production notes).

## Non-goals

- The page's real content: the owner writes it after this step, in `ci/blog`.
- Comments, analytics, search or a newsletter.
- Writing our own generator (an idea for later; Markdown keeps that door open).
- The apex as the canonical name.
- A semantic version for the blog: it ships every commit *(user, 2026-11-18)*.

## Anti-goals

- A person's real data (name, CV, photo) in this public repository. The template holds placeholders only, and the
  content lives in `ci/blog`.
- An image that runs as root, or anything but the site and its server in the final image.
- An unsigned or unverified image on `www`: the blog takes the road like any app, with no shortcut.
- The apex served or redirected by hand outside OpenTofu.

## Constraints

- Latest stable releases, pinned exactly (CLAUDE.md's rules), including inside the template's `Containerfile`.
- The golden path builds with Buildah, unprivileged, with egress 80/443 (`system/egress`). The build's downloads (Hugo,
  the theme) must be pinned and checked, and must work without root.
- The public surface's rules (`docs/how-to-guides/public-surface.md`): the route proxied by Cloudflare, `blog` already
  in the public map with `www`, and `www` already a target of the public scan.
- Production's Backstage starts only after its first release (CLAUDE.md). Until then, creating `ci/blog` from the
  template needs another way, which the guide names (for example Forgejo's own repository template, or a copy).

## Approaches considered

- **Apex canonical, `www` redirecting** *(set aside)*: a shorter address, but it needs an apex listener on the public
  Gateway, the apex on the wildcard certificate, and an apex case in the public map, its check and the scan.
- **Upstream's way**: a web app written for the blog (Rust and Leptos, built through Nix) *(set aside)*. It is heavy
  for an unprivileged Buildah build (tens of minutes and gigabytes to compile in the CI's pod), and it is a site
  rather than a reusable tool.
- **Zola instead of Hugo** *(set aside)*: also one binary, and written in Rust, but it has no ready theme as fit for a
  profile page as PaperMod.
- **A canary for the blog** *(set aside)*: it mixes versions request by request, and a static site's hashed assets break
  across them.
- **Chosen**: Hugo and PaperMod built by the golden path into `static-web-server`, released blue-green, on `www` with an
  apex redirect in OpenTofu.

## Left to the builder

- **How the "coming soon" page is served** before the first promotion. It must never shadow the real site afterwards,
  so a ConfigMap mounted over the real image's root will not do. For example, an image built by this repository, or
  `static-web-server` reading another root that the release replaces.
  *Built (2026-11-24, after the final review):* a ConfigMap page mounted with `subPath` over the base image's
  `/public/index.html` (a directory mount's symlinks are refused by `static-web-server`), turned off by the release
  file Kargo writes on every promotion. This is the shape the line above set aside, so the risk is closed differently:
  the page carries a marker (`homelab-coming-soon`), and `static-site-responds` refuses a preview showing it. A
  release file that left the page mounted is therefore never switched to: `RolloutAborted` fires and the previous
  version stays.
- **How the Containerfile fetches and checks Hugo and the theme**: a release archive with its sha256, or an image
  digest.
- **How Renovate keeps the template's pins current**: a regex manager, or the README saying where they are.
- **The sandbox group's needs**, and whether `Blog` reuses AppDelivery's helpers.
- **What the guide offers for creating `ci/blog` before production's Backstage runs.**
- **How the sandbox's address for the blog relates to production's**: `www.<sandbox domain>` through the LAN Gateway,
  as other public apps are tested.
