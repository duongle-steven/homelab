import { createBackendModule } from '@backstage/backend-plugin-api';
import {
  authProvidersExtensionPoint,
  createOAuthProviderFactory,
} from '@backstage/plugin-auth-node';
import {
  oidcAuthenticator,
  OidcAuthResult,
} from '@backstage/plugin-auth-backend-module-oidc-provider';
import { homelabIdentity } from './identity';

// Kanidm through OIDC (provider id oidc, app-config.yaml's auth.providers.oidc), signed in as homelabIdentity says:
// from the token's name and groups, no catalog User needed
export default createBackendModule({
  pluginId: 'auth',
  moduleId: 'homelab-oidc',
  register(reg) {
    reg.registerInit({
      deps: { providers: authProvidersExtensionPoint },
      async init({ providers }) {
        providers.registerProvider({
          providerId: 'oidc',
          factory: createOAuthProviderFactory({
            authenticator: oidcAuthenticator,
            async signInResolver(info, ctx) {
              const result = info.result.fullProfile as OidcAuthResult;
              const { userEntityRef, ownershipEntityRefs } = homelabIdentity({
                ...result.tokenset.claims(),
                ...result.userinfo,
              });
              return ctx.issueToken({
                claims: { sub: userEntityRef, ent: ownershipEntityRefs },
              });
            },
          }),
        });
      },
    });
  },
});
