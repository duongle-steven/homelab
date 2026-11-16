import { createFrontendModule } from '@backstage/frontend-plugin-api';
import { HomePageWidgetBlueprint } from '@backstage/plugin-home-react/alpha';
import { MarkdownContent } from '@backstage/core-components';

const content = `
The homelab's components, its docs and the templates that start new
repositories:

- **[Catalog](/catalog)**: every app and component, its owner, its
  pods (Kubernetes) and its sync (Argo CD)
- **[Docs](/docs)**: this repository's documentation (TechDocs)
- **[Create](/create)**: a new repository in Forgejo org \`ci\`, built,
  signed and attested by the golden path (admins only)

A description lives in each app's and component's own files: change it
there and run \`lab catalog generate\`.
`;

const gettingStartedWidget = HomePageWidgetBlueprint.make({
  name: 'homelab',
  params: {
    name: 'HomelabIntro',
    title: 'Homelab',
    description: "What is here and where it comes from",
    components: async () => ({
      Content: () => <MarkdownContent content={content} />,
    }),
  },
});

export const homeModule = createFrontendModule({
  pluginId: 'home',
  extensions: [gettingStartedWidget],
});
