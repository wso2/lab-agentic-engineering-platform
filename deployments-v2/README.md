# ASDLC Local Development Setup (v2)

Three scripts to bring up WSO2 Cloud (OpenChoreo + Thunder + OpenBao + ESO + kgateway)
running ASDLC on a local k3d cluster.

## Prerequisites

`setup.sh` checks all of these on start and lists any that are missing.

- **Docker** (Colima or Docker Desktop)
- **k3d** v5.8+ (`brew install k3d`)
- **kubectl** (`brew install kubectl`)
- **yq** v4 — Mike Farah's (`brew install yq`)
- **openssl**, **curl**, **base64**, **envsubst** — usually present; the last is in `gettext` on macOS (`brew install gettext`)
- **git** with access to `wso2-enterprise/wso2cloud-deployment` private repo
  - macOS Keychain: `git config --global credential.helper osxkeychain`
  - Or PAT: `git config --global url."https://<USER>:<PAT>@github.com/".insteadOf "https://github.com/"`

## Webhook delivery

The local cluster has no public ingress, so GitHub webhooks reach the BFF
through a smee.io relay. The relay runs **on the developer's machine**, not
in the cluster — it's a simple host-side script that forwards
`https://smee.io/<channel>` into `kubectl port-forward` and on to the BFF.
Cloud tiers (dev/stage/prod) deliver directly to public ingress and have no
relay; design at `docs/design/webhook-delivery.md`.

```bash
# In a second terminal, after setup.sh has finished:
bash deployments-v2/scripts/webhook-relay.sh
```

The script handles `kubectl port-forward`, the smee-client subprocess,
auto-restart on transient failures, and cleanup on Ctrl-C. Most local dev
(spec/design/code generation) does not require it; it's only needed for
PR/push/merge-driven flows (task-status auto-projection, build-on-merge).

## What's deferred

- **Collab-server** — the WebSocket-driven collaborative editing path in the console is non-functional until `app-factory-collab-server` is added.

## Quick-start

```bash
# 1. Bring up everything (~10-15 mins first time)
#    setup.sh prompts for any missing values (API keys, GitHub PAT, etc.).
#    Press Enter to skip optional values. Auto-generates secrets.
bash deployments-v2/scripts/setup.sh

# 2. Open the console (URL printed in the final banner), e.g.:
#    http://http-app-factory-c-development-....openchoreoapis.localhost:19080
#    Login:  admin@openchoreo.dev / Admin@123

# 4. Rebuild after source changes
bash deployments-v2/scripts/dev-cycle.sh

# 5. Tear down asdlc (cluster stays)
bash deployments-v2/scripts/teardown.sh

# 6. Destroy everything
bash deployments-v2/scripts/teardown.sh --all
```

## Scripts

### `setup.sh [--dry-run]`

Brings up wso2cloud platform + asdlc on a local k3d cluster. Idempotent —
re-run after any failure, it picks up where it left off.

Phases:
1. **Env + submodule** — loads `.env`, auto-generates webhook secret, OAUTH key, task-signing RSA key, smee.io channel; validates ANTHROPIC_API_KEY is set
2. **Cluster** — creates k3d cluster `openchoreo` using submodule's `k3d-config.yaml`
3. **Platform** — `kubectl apply -k` against submodule layers (init/layer-0 → layer-1 → layer-2 → platform domain → release-bindings → app-factory project, which includes postgres)
4. **ASDLC** — seeds OpenBao secrets, builds + imports + applies 5 workloads, registers Thunder OAuth/CORS/streaming, runs `seed-admin-github` (only if `LOCAL_DEV_ADMIN_GITHUB_PAT` is set in `.env` — pre-connects the admin org via the same Connect API the console UI uses)

### `dev-cycle.sh [<component>] [--no-rollout-wait]`

Detects source changes per component using content hashing, rebuilds only what
changed, and patches the running Workload with the new image.

- Without args: checks all 5 components
- With `<component>`: only processes that one (e.g. `app-factory-api`)
- `--no-rollout-wait`: skips `kubectl rollout status` after patching

### `teardown.sh [--all]`

Default: removes asdlc Workloads, postgres resources, k3d images, OpenBao seeds,
and local state (`.env`, keys). The cluster and wso2cloud platform stay running.

`--all`: destroys the entire `openchoreo` k3d cluster (typed-`yes` confirmation).

## Env reference

| Variable | Required | Auto | Description |
|---|---|---|---|
| `ANTHROPIC_API_KEY` | Prompted | No | Anthropic API key for the agents-service and the per-task coding-agent ClusterWorkflow pods. Skip to run without AI. |
| `LOCAL_DEV_ADMIN_GITHUB_PAT` | Prompted | No | Local-dev shortcut: PAT for the admin org. Consumed only by `scripts/lib/seed-admin-github.sh`, which calls the public Connect API. Has no effect on hosted environments. |
| `LOCAL_DEV_ADMIN_GITHUB_OWNER` | Prompted | No | GitHub org/user the admin PAT is scoped to. Only prompted when the PAT is set. |
| `LOCAL_DEV_ADMIN_OUHANDLE` | No | No | Thunder-issued ouHandle for the local-dev admin user (default `default`, matching the Thunder seed's only OU). Pinned as a knob — the only place a tenant name appears in this repo's env / scripts. |
| `GITHUB_WEBHOOK_SECRET` | No | Yes | 32-byte random hex for GitHub webhook HMAC |
| `OAUTH_STATE_SIGNING_KEY` | No | Yes | 32-byte random hex for GitHub App connect CSRF |
| `GITHUB_WEBHOOK_DELIVERY_URL` | No | Yes | URL we register on each GitHub repo. Local: smee.io channel (auto-provisioned). Cloud: public BFF ingress URL. |
| `GITHUB_APP_ID` | No | No | GitHub App ID for App-mode connect (optional) |
| `GITHUB_CLIENT_ID` | No | No | GitHub App OAuth client ID (optional) |
| `GITHUB_CLIENT_SECRET` | No | No | GitHub App OAuth client secret (optional) |
| `GITHUB_APP_SLUG` | No | No | GitHub App URL slug (default: `asdlc-platform`) |
| `GITHUB_APP_PRIVATE_KEY_PATH` | No | No | Path to GitHub App private key PEM (optional) |
| `PUBLIC_THUNDER_URL` | No | No | Thunder IDP browser URL (default: `http://thunder.openchoreo.localhost:8080`) |
| `ADMIN_USERNAME` | No | No | Thunder admin user for setup banner (default: `admin@openchoreo.dev`) |
| `ADMIN_PASSWORD` | No | No | Thunder admin password for setup banner (default: `Admin@123`) |

**Note:** `PUBLIC_CONSOLE_URL` is no longer in `.env` — the setup script discovers the console HTTPRoute hostname from the cluster and registers it in Thunder CORS dynamically.

## Directory layout

```
deployments-v2/
├── README.md                          # this file
├── .env.example                       # template — copy to .env and fill
├── .env                               # gitignored, generated on first setup
├── keys/                              # gitignored, RSA keys
├── wso2cloud-deployment/              # git submodule (local-app-factory branch)
├── manifests/
│   └── env-overlays/                  # per-component env + files spliced into Workloads
│       ├── app-factory-api.yaml
│       ├── app-factory-console.yaml
│       ├── app-factory-git-service.yaml
│       └── app-factory-agents-service.yaml
│       # postgres comes from the submodule's kustomize (app-factory project)
│       # The coding-agent runner image (built from remote-worker/) has no
│       # env-overlay — its env flows in via WorkflowRun parameters at dispatch.
└── scripts/
    ├── setup.sh
    ├── dev-cycle.sh
    ├── teardown.sh
    ├── webhook-relay.sh               # host-side smee.io → cluster relay (local only)
    ├── components.sh                  # registry of 4 long-lived components + 1 runner image
    └── lib/
        ├── ui.sh                      # colored logging
        ├── env.sh                     # .env load + validate + autogen
        ├── submodule.sh               # git submodule ensure
        ├── cluster.sh                 # k3d cluster lifecycle
        ├── platform.sh                # kubectl apply -k layers
        ├── asdlc.sh                   # OpenBao seed + postgres + workloads
        ├── images.sh                  # content_hash + build + import
        └── workload.sh                # render_workload + apply + patch
```

## Troubleshooting

### Submodule clone fails (auth)

The submodule lives in a private repo. If `setup.sh` fails at Phase 0:

```bash
git config --global credential.helper osxkeychain
git ls-remote https://github.com/wso2-enterprise/wso2cloud-deployment.git
# Enter PAT when prompted; credential helper persists it
```

Or use a PAT-based URL override:

```bash
git config --global url."https://<USER>:<PAT>@github.com/".insteadOf "https://github.com/"
```

### Colima / Docker restart → k3d IP swap

If Docker restarts (Colima stop/start, reboot), k3d container IPs can swap,
causing k3s to crash on startup with:

```
level=fatal msg="Failed to start networking: unable to initialize network policy controller"
```

**Recovery (preserves cluster state):**

```bash
# 1. Find the original server IP from k3s logs:
docker logs k3d-openchoreo-server-0 2>&1 | grep "listener.cattle.io/cn-172.18."

# 2. If the IP shown is 172.18.0.3, run:
docker stop k3d-openchoreo-serverlb k3d-openchoreo-server-0
docker network disconnect k3d-openchoreo k3d-openchoreo-serverlb
docker network disconnect k3d-openchoreo k3d-openchoreo-server-0
docker network connect --ip 172.18.0.3 k3d-openchoreo k3d-openchoreo-server-0
docker network connect --ip 172.18.0.4 k3d-openchoreo k3d-openchoreo-serverlb
docker start k3d-openchoreo-server-0
sleep 15
docker start k3d-openchoreo-serverlb
```

### Disk pressure

```bash
docker system prune -af --volumes
```

### Reset everything

```bash
bash deployments-v2/scripts/teardown.sh --all
# Then re-run setup.sh for a clean slate.
```

### OpenBao seeding fails

The script waits for the OpenBao pod to be Ready. If OpenBao is stuck
(commonly on first boot with slow hardware), the seed step fails. Re-run
`setup.sh` — it's idempotent and will retry.
