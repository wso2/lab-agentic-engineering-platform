FROM node:22-alpine

# Runtime dependencies for workspace provisioning and agent operations:
# - git: clone repo, commit, push
# - github-cli (gh): issue/PR management by the agent
# - curl + bash + python3: credential helper scripts and Bash tool
RUN apk add --no-cache \
        git \
        github-cli \
        curl \
        python3 \
        bash \
    && addgroup -S asdlc \
    && adduser -S -G asdlc asdlc \
    && mkdir -p /home/asdlc/asdlc-workspace \
    && chown -R asdlc:asdlc /home/asdlc

WORKDIR /app

# Install dependencies first so the layer caches across source-only edits.
# --include=optional ensures the platform-native Claude binary is installed.
COPY --chown=asdlc:asdlc package.json package-lock.json* ./
RUN npm ci --include=optional

# Copy the rest. .dockerignore excludes node_modules/dist/.git/etc so we
# never ship a duplicate of the dependency tree the previous step already
# produced. --chown writes ownership directly into the layer instead of a
# trailing `chown -R /app` (which would rewrite every file as a fresh
# layer and roughly double the image size).
COPY --chown=asdlc:asdlc . .

USER asdlc
ENTRYPOINT ["npx", "tsx", "src/oneshot.ts"]
