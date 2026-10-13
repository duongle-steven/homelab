# Expose services to the internet

!!! info

    This tutorial is for Cloudflare Tunnel users, please skip if you use port forwarding.

Apply the `./external` layer to create a tunnel if you haven't already,
then attach the app's `HTTPRoute` to the public Gateway `homelab-public` (replace `example.com` with your domain):

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  annotations:
    external-dns.kubernetes.io/cloudflare-proxied: "true"
spec:
  parentRefs:
    - name: homelab-public
      namespace: gateway
      sectionName: https
  hostnames:
    - foo.example.com
# ...
```

external-dns points the hostname at the tunnel (the `homelab-public` Gateway carries the
`external-dns.kubernetes.io/target` annotation), and cloudflared forwards tunnel traffic to that Gateway only.
Routes on the LAN Gateway `homelab` stay off the internet.
