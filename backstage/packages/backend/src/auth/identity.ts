// Who a Kanidm sign-in is in Backstage (docs/superpowers/specs/2026-11-16-backstage-design.md): the short name and
// homelab_groups from Kanidm's token, not a catalog User (people stay out of the catalog: no directory read needed).
// Only homelab-admins and homelab-editors sign in (Kanidm's scopeMap refuses the rest first); the permission policy
// (permissions/policy.ts) reads the groups from the ownership refs issued here
const groups = ['homelab-admins', 'homelab-editors'];
// A Kanidm name: lowercase letters, digits, - and _ (refuses an empty, spaced or domain-qualified value)
const name = /^[a-z0-9][a-z0-9_-]*$/;

export type Claims = {
  preferred_username?: unknown;
  homelab_groups?: unknown;
};

export function homelabIdentity(claims: Claims): {
  userEntityRef: string;
  ownershipEntityRefs: string[];
} {
  const user = claims.preferred_username;
  if (typeof user !== 'string' || !name.test(user)) {
    throw new Error('Kanidm sent no usable name (preferred_username)');
  }
  const member = Array.isArray(claims.homelab_groups)
    ? groups.filter(g => (claims.homelab_groups as unknown[]).includes(g))
    : [];
  if (member.length === 0) {
    throw new Error(
      `${user} is in neither homelab-admins or homelab-editors: Backstage is for whoever runs the homelab`,
    );
  }
  const userEntityRef = `user:default/${user}`;
  return {
    userEntityRef,
    ownershipEntityRefs: [userEntityRef, ...member.map(g => `group:default/${g}`)],
  };
}
