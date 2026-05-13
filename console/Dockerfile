# Stage 1: Build the React application.
# Build context is the repo root so workspace packages (ui-components/*) are
# reachable from console's workspace:* dependency on @asdlc/md-editor.
FROM node:22-alpine AS builder

# Pin pnpm to match the local development version. The default corepack
# pnpm in node:22-alpine predates `onlyBuiltDependencies` in
# pnpm-workspace.yaml (a v10.6+ feature), so without this pin the build
# fails with ERR_PNPM_IGNORED_BUILDS for transitive scripts.
RUN corepack enable && corepack prepare pnpm@10.12.3 --activate

WORKDIR /repo

COPY pnpm-workspace.yaml pnpm-lock.yaml ./
COPY console/package.json ./console/
COPY ui-components/md-editor/package.json ./ui-components/md-editor/
COPY ui-components/excalidraw-editor/package.json ./ui-components/excalidraw-editor/
COPY ui-components/explorer/package.json ./ui-components/explorer/
COPY ui-components/cell-diagram-view/package.json ./ui-components/cell-diagram-view/
COPY ui-components/project-status/package.json ./ui-components/project-status/
RUN pnpm install --frozen-lockfile

COPY console/ ./console/
COPY ui-components/ ./ui-components/

RUN pnpm --filter asdlc-console build

# Stage 2: Serve with nginx
FROM nginx:1.28-alpine

USER root

# Pull latest patched versions of CVE-flagged packages. Scoped (not blanket
# `apk upgrade`) so alpine-baselayout's pre-upgrade script doesn't try to clean
# /var/run when this image is built inside a pod that bind-mounts it read-only.
RUN apk upgrade --no-cache \
      libcrypto3 libssl3 libexpat libpng libxml2 \
      musl musl-utils nghttp2-libs zlib

RUN addgroup -g 10001 -S asdlcuser && \
    adduser -u 10001 -S asdlcuser -G asdlcuser

COPY --chown=10001:10001 console/nginx.conf /etc/nginx/nginx.conf
COPY --from=builder --chown=10001:10001 /repo/console/dist /usr/share/nginx/html

COPY --chown=10001:10001 console/docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh

RUN chown -R 10001:10001 /var/cache/nginx && \
    chown -R 10001:10001 /var/log/nginx && \
    chown -R 10001:10001 /etc/nginx && \
    chown -R 10001:10001 /usr/share/nginx/html

USER 10001

EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:3000/ || exit 1

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
