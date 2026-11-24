# The blog

`www.hoangduong.dev` is the homelab's public personal page and blog: a Hugo site with the PaperMod theme, whose home page
introduces its owner (PaperMod's profile mode) and whose posts sit below. It is the first app on the app road
([App delivery](app-delivery.md)): every push to its repository becomes a new version, built, signed, verified,
promoted and released with no one approving it (`docs/superpowers/specs/2026-11-21-blog-design.md`).

## The address

`www.<domain>` is the blog's one name. The apex (`hoangduong.dev`) redirects there permanently, keeping the path and
query, through a Cloudflare Redirect Rule in OpenTofu ([Public surface](public-surface.md)). The route is proxied by
Cloudflare and published through the Tunnel like every public app.

## Before there is content

Until `ci/blog` exists, `www` shows a small "coming soon" page: `apps/blog.yaml` mounts it over its server's root. The
first promotion's release file (`ops/releases/blog.yaml`) turns that page off, so it never covers the site.

## Creating the repository, once

Create it only when the first real content is ready: the template's placeholder text would otherwise go public on the
first build.

- **From Backstage** (`https://backstage.<domain>`, `homelab-admins`): Create → "A Hugo site in org ci", name **`blog`**.
  The name matters: Kargo's Warehouse watches `registry.<domain>/blog`, and the CI publishes a repository's image only
  under its own name.
- **Before production's Backstage runs** (it waits for its first release, CLAUDE.md): copy
  `backstage/templates/hugo-site/skeleton/` into a new private repository `ci/blog` in Forgejo, replacing
  `${{ values.name }}` with `blog` and `${{ values.description }}` with a line about the site.

## Writing

- The profile: `hugo.toml`'s `params.profileMode` (title, subtitle, buttons) and `params.description`.
- A post: a Markdown file in `content/posts/` with its front matter (title, date, tags). It is published once its
  `draft` is false.
- Images and files: `static/` (served at the site's root) or beside a post in a page bundle.
- Preview on the workstation: install Hugo extended and PaperMod at the versions the `Containerfile` names, then
  `hugo server -D` (`-D` shows drafts) and open `http://localhost:1313`.
- Edit in Forgejo's web editor when away from the workstation: every commit to `main` publishes.

Content is Markdown, Hugo's own format: easy to write and to edit anywhere, and the engine can be swapped later
without losing a post.

## What a push does

1. The golden path ([in-cluster CI](in-cluster-ci.md)) builds the `Containerfile`: Hugo (its archive checked by sha256)
   renders the site with PaperMod (a pinned commit, checked the same way), and the image keeps only the site and
   `static-web-server`, as a non-root user on port 8080. It pushes `registry.<domain>/blog:<commit>`, signs it and
   attests it.
2. Kargo's Warehouse makes Freight of it, Stage `verify` checks the signature, and Stage `production` writes
   `ops/releases/blog.yaml`.
3. Argo Rollouts releases it **blue-green**, not as a canary: PaperMod names its stylesheet by a hash of its content,
   so mixing two versions request by request would load pages unstyled. The new version comes up behind
   `blog-preview`, where `static-site-responds` fetches its page and every stylesheet, script, icon and image it names.
   On any non-2xx the switch never happens. Otherwise every visitor moves at once, and `gateway-success-rate` watches
   the live version: if it fails, everyone goes back to the previous version, which stays up five minutes for that.
   `RolloutAborted` reports either failure.

## Upgrading Hugo, the theme or the server

Change the version and its sha256 in `ci/blog`'s `Containerfile` (Hugo's release lists checksums; for the theme, hash
the archive of the new commit), and in `backstage/templates/hugo-site/skeleton/` so a new site starts current.
`apps/blog.yaml`'s base image (the "coming soon" server) follows `static-web-server`'s version too.
