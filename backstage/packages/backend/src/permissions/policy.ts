import { UserInfoService } from '@backstage/backend-plugin-api';
import {
  AuthorizeResult,
  PolicyDecision,
} from '@backstage/plugin-permission-common';
import {
  createScaffolderActionConditionalDecision,
  scaffolderActionConditions,
} from '@backstage/plugin-scaffolder-backend/alpha';
import { actionExecutePermission } from '@backstage/plugin-scaffolder-common/alpha';
import {
  PermissionPolicy,
  PolicyQuery,
  PolicyQueryUser,
} from '@backstage/plugin-permission-node';

const admins = 'group:default/homelab-admins';
const editors = 'group:default/homelab-editors';

// What an editor may not do: run templates, and add or remove what the catalog reads (the threat model's
// scaffolder concern; docs/superpowers/specs/2026-11-16-backstage-design.md)
const adminOnly = new Set([
  'scaffolder.task.create',
  'scaffolder.action.execute',
  'scaffolder.template.management',
  'scaffolder.template.dry-run',
  'catalog.location.create',
  'catalog.location.delete',
  'catalog.entity.create',
  'catalog.entity.delete',
]);

// The only actions a template may run, even for an admin: fetch, publish to Forgejo, register in the catalog, log
// (the threat model: no shell, no other publisher)
export const allowedActions = [
  'fetch:template',
  'fetch:plain',
  'fetch:plain:file',
  'publish:gitea',
  'catalog:register',
  'debug:log',
];

const [first, ...rest] = allowedActions;
type Condition = ReturnType<typeof scaffolderActionConditions.hasActionId>;

// Only homelab-admins and homelab-editors sign in (Kanidm's scopeMap); a request from anyone else, or with no user,
// is refused here too
export class HomelabPermissionPolicy implements PermissionPolicy {
  constructor(private readonly userInfo: UserInfoService) {}

  async handle(
    request: PolicyQuery,
    user?: PolicyQueryUser,
  ): Promise<PolicyDecision> {
    if (!user) {
      return { result: AuthorizeResult.DENY };
    }
    const { ownershipEntityRefs } = await this.userInfo.getUserInfo(
      user.credentials,
    );
    if (ownershipEntityRefs.includes(admins)) {
      if (request.permission.name === actionExecutePermission.name) {
        return createScaffolderActionConditionalDecision(
          actionExecutePermission,
          {
            anyOf: [first, ...rest].map(actionId =>
              scaffolderActionConditions.hasActionId({ actionId }),
            ) as [Condition, ...Condition[]],
          },
        );
      }
      return { result: AuthorizeResult.ALLOW };
    }
    if (
      ownershipEntityRefs.includes(editors) &&
      !adminOnly.has(request.permission.name)
    ) {
      return { result: AuthorizeResult.ALLOW };
    }
    return { result: AuthorizeResult.DENY };
  }
}
