FROM node:22-alpine

# Runtime dependencies for workspace provisioning and agent operations:
# - git: clone feature branches, commit, push
# - github-cli (gh): issue/PR management by the agent
# - curl + bash + python3: credential helper scripts
# - bash: shell executor for Bash tool + credhelper scripts
RUN apk add --no-cache \
    git \
    github-cli \
    curl \
    python3 \
    bash

WORKDIR /app
COPY package.json package-lock.json* ./
# --include=optional ensures the platform-native Claude binary is installed
RUN npm install --include=optional
COPY . .

# Claude Code refuses to run as root with --dangerously-skip-permissions.
# Create a non-root user with a writeable home for workspace provisioning.
RUN addgroup -S asdlc && adduser -S -G asdlc asdlc
RUN chown -R asdlc:asdlc /app
RUN mkdir -p /home/asdlc && chown -R asdlc:asdlc /home/asdlc
USER asdlc

EXPOSE 3200
CMD ["npx", "tsx", "src/index.ts"]
