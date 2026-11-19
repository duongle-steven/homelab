import {
  coreServices,
  createBackendModule,
} from '@backstage/backend-plugin-api';
import { policyExtensionPoint } from '@backstage/plugin-permission-node/alpha';
import { HomelabPermissionPolicy } from './policy';

export default createBackendModule({
  pluginId: 'permission',
  moduleId: 'homelab-policy',
  register(reg) {
    reg.registerInit({
      deps: { policy: policyExtensionPoint, userInfo: coreServices.userInfo },
      async init({ policy, userInfo }) {
        policy.setPolicy(new HomelabPermissionPolicy(userInfo));
      },
    });
  },
});
