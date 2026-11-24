# ${{ values.name }}

${{ values.description }}

A Hugo site with the PaperMod theme. Every push to `main` is built by the in-cluster CI, signed, and released by the
app road (the homelab's `docs/how-to-guides/blog.md`).

- The profile page: `hugo.toml`'s `params.profileMode`.
- A post: a Markdown file in `content/posts/`; it is published once its `draft: false`.
- Preview: install Hugo extended and PaperMod (the `Containerfile` names the versions) and run `hugo server -D`.
