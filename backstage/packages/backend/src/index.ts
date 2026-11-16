// The homelab's Backstage backend (docs/superpowers/specs/2026-11-16-backstage-design.md): sign-in through Kanidm,
// people and groups from Kanidm's LDAP, the catalog the repository describes, TechDocs, templates publishing to
// Forgejo, and read-only views of Kubernetes and Argo CD
import { createBackend } from '@backstage/backend-defaults';

const backend = createBackend();

backend.add(import('@backstage/plugin-app-backend'));
backend.add(import('@backstage/plugin-proxy-backend'));

// Sign-in: Kanidm through OIDC, resolved to a catalog User (no guest, no sign-in without one)
backend.add(import('@backstage/plugin-auth-backend'));
backend.add(import('@backstage/plugin-auth-backend-module-oidc-provider'));

// Catalog: the repository's catalog-info.yaml (lab catalog generate) and Kanidm's people and groups
backend.add(import('@backstage/plugin-catalog-backend'));
backend.add(
  import('@backstage/plugin-catalog-backend-module-scaffolder-entity-model'),
);
backend.add(import('@backstage/plugin-catalog-backend-module-logs'));
backend.add(import('@backstage/plugin-catalog-backend-module-ldap'));
// A component's homelab/host becomes the link to its page (catalog/hostLinks.ts)
backend.add(import('./catalog/module'));

// Permissions: templates only for homelab-admins (permissions/policy.ts)
backend.add(import('@backstage/plugin-permission-backend'));
backend.add(import('./permissions/module'));

// Templates publish to Forgejo (Gitea's API); no other publisher
backend.add(import('@backstage/plugin-scaffolder-backend'));
backend.add(import('@backstage/plugin-scaffolder-backend-module-gitea'));
backend.add(
  import('@backstage/plugin-scaffolder-backend-module-notifications'),
);

backend.add(import('@backstage/plugin-techdocs-backend'));

// Search on PostgreSQL
backend.add(import('@backstage/plugin-search-backend'));
backend.add(import('@backstage/plugin-search-backend-module-pg'));
backend.add(import('@backstage/plugin-search-backend-module-catalog'));
backend.add(import('@backstage/plugin-search-backend-module-techdocs'));

// Read-only views: Kubernetes (a ClusterRole without Secrets, logs or exec) and Argo CD (account backstage, role
// readonly)
backend.add(import('@backstage/plugin-kubernetes-backend'));
backend.add(import('@roadiehq/backstage-plugin-argo-cd-backend'));

backend.add(import('@backstage/plugin-user-settings-backend'));
backend.add(import('@backstage/plugin-notifications-backend'));
backend.add(import('@backstage/plugin-signals-backend'));

backend.start();
