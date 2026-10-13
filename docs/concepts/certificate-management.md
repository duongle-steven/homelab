# Certificate management

Certificates are generated and managed by [cert-manager](https://cert-manager.io) with [Let's Encrypt](https://letsencrypt.org).
By default certificates are valid for 90 days and will be renewed after 60 days.

Apps do not request certificates. One wildcard `Certificate` in namespace `gateway` (`system/gateway/templates/certificate.yaml`) covers every app host, and both Gateways terminate TLS with its secret:

```yaml hl_lines="6 8"
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: homelab-wildcard
spec:
  secretName: homelab-wildcard-tls
  dnsNames:
    - "*.example.com"
  issuerRef:
    kind: ClusterIssuer
    name: letsencrypt-prod
```

Kanidm also has its own `Certificate`, because it serves TLS itself: the Gateway re-encrypts to it and checks its certificate (`BackendTLSPolicy`).

```mermaid
flowchart LR
  User -- 6 --> Gateway

  subgraph cluster[Homelab cluster]
    Gateway --- Secret
    Certificate -. 5 .-> Secret
    Certificate -- 2 --> CertificateRequest -- 3 --> Order -- 4 --> Challenge
  end

  Order -.- ACMEServer[ACME server]

  subgraph dnsprovider[DNS provider]
    TXT
  end

  Challenge -- 4.a --> TXT
  ACMEServer -.- Challenge
  ACMEServer -. 4.b .-> TXT
```

1. The `Certificate` resource is in git (`system/gateway`)
2. Based on the `Certificate` resource, cert-manager creates a `CertificateRequest` resource to request a signed certificate from the configured `ClusterIssuer`
3. The `CertificateRequest` will create an order with an ACME server (we use Let's Encrypt), which is represented by the `Order` resource
4. Then cert-manager will perform a [DNS-01](https://cert-manager.io/docs/configuration/acme/dns01) `Challenge`:
    1. Create a DNS TXT record (contains a computed key)
    2. The ACME server retrieve this key via a DNS lookup and validate that we own the domain for the requested certificate
7. cert-manager stores the certificate (typically `tls.crt` and `tls.key`) in the `Secret` named in the `Certificate`, which the Gateways reference
8. Now you can access the HTTPS website with a valid certificate

A much more detailed diagram can be found in the official documentation under [certificate lifecycle](https://cert-manager.io/docs/concepts/certificate/#certificate-lifecycle).
