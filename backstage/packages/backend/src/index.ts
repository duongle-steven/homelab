// The homelab's Backstage backend (docs/superpowers/specs/2026-11-16-backstage-design.md): sign-in through Kanidm,
// the catalog the repository describes, TechDocs, templates publishing to
// Forgejo, and read-only views of Kubernetes and Argo CD
import { createBackend } from '@backstage/backend-defaults';

const backend = createBackend();

backend.add(import('@backstage/plugin-app-backend'));
backend.add(import('@backstage/plugin-proxy-backend'));

// Sign-in: Kanidm through OIDC, the identity from its token's name and groups (auth/identity.ts); no guest
backend.add(import('@backstage/plugin-auth-backend'));
backend.add(import('./auth/module'));

// Catalog: the repository's catalog-info.yaml (lab catalog generate), groups included; people stay out of it
backend.add(import('@backstage/plugin-catalog-backend'));
backend.add(
  import('@backstage/plugin-catalog-backend-module-scaffolder-entity-model'),
);
backend.add(import('@backstage/plugin-catalog-backend-module-logs'));
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

// A plugin that fails at start (PostgreSQL not ready after a node restart or a power cut) leaves the process up with
// its liveness probe passing, never retrying: exit instead, so Kubernetes restarts the pod until the database answers
backend.start().catch(error => {
  console.error('Backend startup failed, exiting:', error);
  process.exit(1);
});
