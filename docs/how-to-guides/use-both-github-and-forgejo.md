# Use both GitHub and Forgejo

Forgejo is the source of truth: Argo CD syncs from `ops/homelab` on Forgejo.
GitHub keeps a copy for backup and discovery; push to it when needed (an automatic push mirror comes in step 10).

Add both push URLs (replace my repositories with yours):

```sh
git remote set-url --add --push origin git@git.khuedoan.com:ops/homelab
git remote set-url --add --push origin git@github.com:khuedoan/homelab
```

Now `git push` pushes to both Forgejo and GitHub.
