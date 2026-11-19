import { HostLinksProcessor } from './hostLinks';

const location = { type: 'url', target: 'https://example/catalog-info.yaml' };
const component = (annotations: Record<string, string>) => ({
  apiVersion: 'backstage.io/v1alpha1',
  kind: 'Component',
  metadata: { name: 'matrix-client', annotations },
  spec: {},
});

describe('HostLinksProcessor', () => {
  const processor = new HostLinksProcessor('127-0-0-1.nip.io');

  it("links a component to its page on the environment's domain", async () => {
    const entity = await processor.preProcessEntity(
      component({ 'homelab/host': 'chat' }),
      location,
    );
    expect(entity.metadata.links).toEqual([
      { url: 'https://chat.127-0-0-1.nip.io', title: 'Open', icon: 'web' },
    ]);
  });

  it('leaves an entity without a host as it is', async () => {
    const entity = await processor.preProcessEntity(component({}), location);
    expect(entity.metadata.links).toBeUndefined();
  });
});
