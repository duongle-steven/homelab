import {
  BackstageCredentials,
  BackstageUserPrincipal,
} from '@backstage/backend-plugin-api';
import { AuthorizeResult } from '@backstage/plugin-permission-common';
import { mockCredentials } from '@backstage/backend-test-utils';
import {
  catalogEntityCreatePermission,
  catalogEntityDeletePermission,
  catalogEntityReadPermission,
  catalogEntityRefreshPermission,
  catalogLocationCreatePermission,
  catalogLocationDeletePermission,
} from '@backstage/plugin-catalog-common/alpha';
import {
  actionExecutePermission,
  taskCreatePermission,
  templateDryRunPermission,
} from '@backstage/plugin-scaffolder-common/alpha';
import { HomelabPermissionPolicy } from './policy';

// Kanidm's groups reach the catalog through LDAP: a person is admin or editor by membership
const groups: Record<string, string[]> = {
  'user:default/ada': ['user:default/ada', 'group:default/homelab-admins'],
  'user:default/eve': ['user:default/eve', 'group:default/homelab-editors'],
  'user:default/kid': ['user:default/kid', 'group:default/homelab-users'],
};

const policy = new HomelabPermissionPolicy({
  getUserInfo: async (
    credentials: BackstageCredentials<BackstageUserPrincipal>,
  ) => ({
    userEntityRef: credentials.principal.userEntityRef,
    ownershipEntityRefs: groups[credentials.principal.userEntityRef] ?? [],
  }),
} as any);

const decide = async (permission: any, user: string) =>
  (
    await policy.handle(
      { permission },
      {
        credentials: mockCredentials.user(user),
        info: { userEntityRef: user, ownershipEntityRefs: [] },
      },
    )
  ).result;

describe('HomelabPermissionPolicy', () => {
  it('lets admins execute templates', async () => {
    expect(await decide(taskCreatePermission, 'user:default/ada')).toBe(
      AuthorizeResult.ALLOW,
    );
  });

  it('lets admins run only the actions the templates need', async () => {
    const decision = await policy.handle(
      { permission: actionExecutePermission },
      {
        credentials: mockCredentials.user('user:default/ada'),
        info: { userEntityRef: 'user:default/ada', ownershipEntityRefs: [] },
      },
    );
    expect(decision.result).toBe(AuthorizeResult.CONDITIONAL);
    const ids = JSON.stringify(decision).match(/"actionId":"[^"]+"/g);
    expect(ids?.sort()).toEqual(
      [
        'catalog:register',
        'debug:log',
        'fetch:plain',
        'fetch:plain:file',
        'fetch:template',
        'publish:gitea',
      ].map(id => `"actionId":"${id}"`),
    );
  });

  it('refuses templates to editors', async () => {
    expect(await decide(taskCreatePermission, 'user:default/eve')).toBe(
      AuthorizeResult.DENY,
    );
    expect(await decide(actionExecutePermission, 'user:default/eve')).toBe(
      AuthorizeResult.DENY,
    );
    // A dry run runs the template's actions too
    expect(await decide(templateDryRunPermission, 'user:default/eve')).toBe(
      AuthorizeResult.DENY,
    );
  });

  it('lets editors read and refresh, not register or unregister', async () => {
    for (const allowed of [
      catalogEntityReadPermission,
      catalogEntityRefreshPermission,
    ]) {
      expect(await decide(allowed, 'user:default/eve')).toBe(
        AuthorizeResult.ALLOW,
      );
    }
    for (const denied of [
      catalogLocationCreatePermission,
      catalogLocationDeletePermission,
      catalogEntityCreatePermission,
      catalogEntityDeletePermission,
    ]) {
      expect(await decide(denied, 'user:default/eve')).toBe(
        AuthorizeResult.DENY,
      );
    }
  });

  it('refuses everything to anyone neither admin nor editor', async () => {
    expect(await decide(catalogEntityReadPermission, 'user:default/kid')).toBe(
      AuthorizeResult.DENY,
    );
  });

  it('refuses a request without a user', async () => {
    expect(
      (await policy.handle({ permission: catalogEntityReadPermission })).result,
    ).toBe(AuthorizeResult.DENY);
  });
});
