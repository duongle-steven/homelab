import {
  coreServices,
  createBackendModule,
} from '@backstage/backend-plugin-api';
import { catalogProcessingExtensionPoint } from '@backstage/plugin-catalog-node';
import { HostLinksProcessor } from './hostLinks';

export default createBackendModule({
  pluginId: 'catalog',
  moduleId: 'homelab-host-links',
  register(reg) {
    reg.registerInit({
      deps: {
        catalog: catalogProcessingExtensionPoint,
        config: coreServices.rootConfig,
      },
      async init({ catalog, config }) {
        catalog.addProcessor(
          new HostLinksProcessor(config.getString('homelab.domain')),
        );
      },
    });
  },
});
