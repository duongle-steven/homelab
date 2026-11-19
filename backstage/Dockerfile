# Backstage's image (docs/superpowers/specs/2026-11-16-backstage-design.md): built from this directory, by
# scripts/build-backstage-image in the sandbox and by .github/workflows/backstage-image.yaml for production (signed
# keyless, its SBOM attested; Kyverno's verify-backstage-image checks both)

# Every dependency, the types, the frontend and the backend's bundle
FROM node:24.22.3-trixie-slim AS build
WORKDIR /app
COPY . .
RUN yarn install --immutable && yarn tsc && yarn build:backend

# The runtime: Node, the backend's production dependencies, and MkDocs with techdocs-core for TechDocs' local
# builder (this repository's docs use mkdocs-material and pymdownx, which techdocs-core pins)
FROM node:24.22.3-trixie-slim
RUN apt-get update && \
    apt-get install -y --no-install-recommends python3 python3-venv && \
    rm -rf /var/lib/apt/lists/* && \
    python3 -m venv /opt/mkdocs && \
    /opt/mkdocs/bin/pip install --no-cache-dir mkdocs-techdocs-core==1.7.1
ENV PATH=/opt/mkdocs/bin:$PATH \
    NODE_ENV=production
# Everything from here as the image's own user (node), by number: Pod Security restricted (runAsNonRoot) checks it
# without a name, and the files it unpacks are its own
RUN mkdir /app && chown 1000:1000 /app
USER 1000
WORKDIR /app
COPY --from=build --chown=1000:1000 /app/.yarn ./.yarn
COPY --from=build --chown=1000:1000 /app/.yarnrc.yml /app/backstage.json /app/yarn.lock /app/package.json ./
COPY --from=build --chown=1000:1000 /app/packages/backend/dist/skeleton.tar.gz ./
RUN tar xzf skeleton.tar.gz && rm skeleton.tar.gz && \
    yarn workspaces focus --all --production && rm -rf "$(yarn cache clean)"
COPY --from=build --chown=1000:1000 /app/packages/backend/dist/bundle.tar.gz ./
RUN tar xzf bundle.tar.gz && rm bundle.tar.gz
COPY --chown=1000:1000 app-config.yaml app-config.production.yaml packages/backend/src/instrumentation.js ./
CMD ["node", "--require", "./instrumentation.js", "packages/backend", "--config", "app-config.yaml", "--config", "app-config.production.yaml"]
