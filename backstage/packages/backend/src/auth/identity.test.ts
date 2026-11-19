import { homelabIdentity } from './identity';

// Kanidm's ID token and userinfo carry the short name (preferShortUsername) and homelab_groups (the client's claimMap)
describe('homelabIdentity', () => {
  it('makes an admin a user owned by homelab-admins', () => {
    expect(
      homelabIdentity({
        preferred_username: 'dev-admin',
        homelab_groups: ['homelab-admins'],
      }),
    ).toEqual({
      userEntityRef: 'user:default/dev-admin',
      ownershipEntityRefs: [
        'user:default/dev-admin',
        'group:default/homelab-admins',
      ],
    });
  });

  it('makes an editor a user owned by homelab-editors', () => {
    expect(
      homelabIdentity({
        preferred_username: 'dev-editor',
        homelab_groups: ['homelab-editors'],
      }).ownershipEntityRefs,
    ).toEqual(['user:default/dev-editor', 'group:default/homelab-editors']);
  });

  it('keeps only the groups Backstage knows', () => {
    expect(
      homelabIdentity({
        preferred_username: 'dev-admin',
        homelab_groups: ['homelab-admins', 'homelab-users', 'other'],
      }).ownershipEntityRefs,
    ).toEqual(['user:default/dev-admin', 'group:default/homelab-admins']);
  });

  it('refuses anyone neither admin nor editor', () => {
    expect(() =>
      homelabIdentity({
        preferred_username: 'dev-viewer',
        homelab_groups: ['homelab-users'],
      }),
    ).toThrow(/homelab-admins or homelab-editors/);
    expect(() =>
      homelabIdentity({ preferred_username: 'dev-outsider' }),
    ).toThrow();
  });

  it('refuses a token without a usable name', () => {
    expect(() =>
      homelabIdentity({ homelab_groups: ['homelab-admins'] }),
    ).toThrow(/name/);
    expect(() =>
      homelabIdentity({
        preferred_username: 'Not A Name',
        homelab_groups: ['homelab-admins'],
      }),
    ).toThrow(/name/);
  });
});
