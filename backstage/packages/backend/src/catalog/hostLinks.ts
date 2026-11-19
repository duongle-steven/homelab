import { Entity } from '@backstage/catalog-model';
import { LocationSpec } from '@backstage/plugin-catalog-common';
import { CatalogProcessor } from '@backstage/plugin-catalog-node';

// git holds no domain (CLAUDE.md): lab catalog generate writes a component's host as its first label
// (homelab/host), and this adds the link to its page on the environment's domain (homelab.domain)
export class HostLinksProcessor implements CatalogProcessor {
  constructor(private readonly domain: string) {}

  getProcessorName() {
    return 'HomelabHostLinksProcessor';
  }

  async preProcessEntity(entity: Entity, _location: LocationSpec) {
    const host = entity.metadata.annotations?.['homelab/host'];
    if (!host) {
      return entity;
    }
    return {
      ...entity,
      metadata: {
        ...entity.metadata,
        links: [
          ...(entity.metadata.links ?? []),
          { url: `https://${host}.${this.domain}`, title: 'Open', icon: 'web' },
        ],
      },
    };
  }
}
