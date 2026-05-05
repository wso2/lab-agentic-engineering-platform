# GitHub Webhook Delivery — Local vs Cloud

## Problem

Today the platform has one webhook-delivery model — smee.io as a public relay
into the in-cluster BFF — and that model is encoded into shared config
(`GITHUB_WEBHOOK_PROXY_URL`, `secret-references/github-webhook.yaml`,
`scripts/lib/env.sh`) and into the `wso2cloud-deployment` submodule as a
"deferred cloud component" (`app-factory-local/SMEE-CLIENT.md`). It exists
because the local k3d cluster has no public ingress, but it leaks into the
WSO2 Cloud (dev/stage/prod) story where the BFF *does* have public ingress
and GitHub can deliver directly.

We want the boundary explicit: cloud is one delivery model, local is a
different one, and nothing about the local model intrudes on the cloud
manifests or vice versa. A new tier should be configured by setting one
URL; smee should never appear in the same mental model as production.

## Goals

1. Cloud envs deliver webhooks straight from GitHub to the BFF over public
   HTTPS — no smee.io, no smee-client, no relay infrastructure.
2. Local k3d retains a smee-based relay, but as a **developer-side process**
   on the host machine — not as a cluster workload, OC Component, or any
   manifest under `wso2cloud-deployment/`.
3. The two paths share one piece of config: the URL we register on each
   GitHub repo. Nothing else.
4. The local relay is a single command for the developer — the script
   handles port-forwarding, prerequisite checks, restarts on transient
   failures, and cleanup.

## Non-goals

- Not redesigning the BFF webhook receiver, projector, or HMAC validation —
  those are delivery-path-agnostic and stay as-is.
- Not changing per-repo registration logic in git-service —
  `webhook_service.go` is already URL-agnostic.

## Design

### One contract: `GITHUB_WEBHOOK_DELIVERY_URL`

The single piece of config is the URL we tell GitHub to POST to. git-service
registers exactly this URL on each repo's webhook config. The BFF does not
read it.

| Tier | `GITHUB_WEBHOOK_DELIVERY_URL` points at | What forwards to BFF |
|------|-----------------------------------------|----------------------|
| local | `https://smee.io/<auto-channel>` | host-side relay script |
| dev | `https://<bff-public-host>/webhooks/github` | nothing — direct |
| stage | `https://<bff-public-host>/webhooks/github` | nothing — direct |
| prod | `https://<bff-public-host>/webhooks/github` | nothing — direct |

Hard rename from `GITHUB_WEBHOOK_PROXY_URL` (no soft alias — only callers
are this repo and the submodule, both of which we control).

### Cloud delivery path

GitHub → kgateway HTTPRoute (public, TLS-terminated) → `app-factory-api:8080/webhooks/github`.

The HTTPRoute lives in the `wso2cloud-deployment` submodule alongside every
other public ingress concern (Thunder, console, OC API):

```yaml
# domains/platform/.../app-factory-api/httproute-webhooks.yaml (per env branch)
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: { name: app-factory-api-webhooks }
spec:
  hostnames: ["<bff-public-host>"]
  rules:
    - matches: [{ path: { type: PathPrefix, value: /webhooks/github } }]
      backendRefs: [{ name: app-factory-api, port: 8080 }]
```

### Local delivery path

GitHub → smee.io → **host-side `webhook-relay.sh`** → `kubectl port-forward` → in-cluster BFF.

Nothing in the cluster relays anything. The smee-client runs as a node
process on the developer's machine, started by an opt-in script. The
`wso2cloud-deployment` submodule has zero smee references in any branch.

#### `deployments-v2/scripts/webhook-relay.sh`

The script encapsulates every piece of complexity so the developer types
one command. Responsibilities, in order:

1. **Prerequisite checks.** Verify `kubectl`, `node`, and `npx` are on
   PATH. Verify the k3d context exists (`k3d-openchoreo`) and the BFF
   service is up (`kubectl get svc app-factory-api`). Verify
   `deployments-v2/.env` contains `GITHUB_WEBHOOK_DELIVERY_URL` matching
   `https://smee.io/...`. Each check fails with an actionable message
   (e.g. "no smee channel — run `bash deployments-v2/scripts/setup.sh`
   first").
2. **Single-instance guard.** Detect an existing relay (PID file at
   `deployments-v2/.webhook-relay.pid`). Verify the PID is alive with
   `kill -0` before refusing — a stale PID file from a crashed previous
   run must not wedge the next start.
3. **Port-forward with auto-restart.** Background
   `kubectl --context k3d-openchoreo port-forward svc/app-factory-api 18080:8080`,
   restart on exit until the parent script terminates. Log to
   `deployments-v2/.webhook-relay.log`. Port 18080 is chosen deliberately
   to avoid collision with common dev servers; if already bound, fail
   loudly with the offending process name (`lsof -i :18080`).
4. **smee-client with auto-restart.** Run
   `npx --yes smee-client --url "$GITHUB_WEBHOOK_DELIVERY_URL" --target http://localhost:18080/webhooks/github`,
   restart on transient failures (network blips, smee.io reconnects).
   First run downloads ~20MB on cold npx cache; print
   `fetching smee-client…` so the wait doesn't look like a hang.
5. **Clean shutdown.** `trap` on EXIT/INT/TERM kills the port-forward,
   removes the PID file, prints a one-line summary of events handled.
6. **Friendly status output.** On startup, print:
   ```
   webhook relay
     channel : https://smee.io/abcd1234
     target  : http://localhost:18080/webhooks/github
     bff     : svc/app-factory-api:8080 (k3d-openchoreo)
     replay  : open https://smee.io/abcd1234 to redeliver missed events
   listening for events…
   ```

The script is foreground-only — Ctrl-C is the documented way to stop it.
No daemon, no systemd, no LaunchAgent. If the developer needs it across
sessions, they run it under `tmux` or `screen` like any other dev tool.

#### Discoverability

`setup.sh` closing banner gains a single line:

```
webhook relay (optional, for PR/push/merge testing):
  bash deployments-v2/scripts/webhook-relay.sh
```

The same line goes into `deployments-v2/README.md` under "Running locally."
Most developers never need it — agent dispatch, code generation, and PR
creation work without webhooks. Only task-status auto-projection and
build-on-merge require it.

#### Why smee.io still, and not ngrok / cloudflared

smee.io is HMAC-content-aware (passes through GitHub's `X-Hub-Signature-256`
unmodified), persistent (channel URL is stable across cluster rebuilds, so
existing repo webhook registrations don't go stale), and free with no auth.
ngrok and cloudflared work but rotate URLs on free tiers and require account
setup. smee fits the "register once, use forever in local" model.

smee.io has no SLA and has had multi-hour outages. Outages block local
webhook testing only — cloud delivery is direct and unaffected. Acceptable
for a dev-only path.

#### Why not just expose the BFF through the local k3d gateway

Adding an HTTPRoute on the local kgateway would let the relay target a
hostname instead of a port-forward, but it does not remove the relay:
smee.io still cannot reach the local gateway (it's not public either), so
something on the developer's host has to bridge `smee.io → local cluster`.
Given a host-side process is required regardless, port-forward is the
smaller dependency — no HTTPRoute, no local TLS, no DNS rewrite.

### App-mode tier strategy

A GitHub App has one webhook URL configured at registration. That's
incompatible with sharing one App across three tiers with different hosts.
**One GitHub App per tier**: `asdlc-platform-dev`, `asdlc-platform-stage`,
`asdlc-platform`. Each App's webhook URL is pinned to its tier's
`<bff-public-host>/webhooks/github`. Local development uses PAT mode only.

git-service is already aware of the WebhookStrategy distinction
(`webhook_service.go:76`) and short-circuits per-repo registration in App
mode — no code change needed for the App-mode path.

### Stable-URL invariant

Once a tier is live, its `<bff-public-host>` should not change: existing
repos have hooks pinned to it, and rotating the host orphans them. This
is the operational rule.

If a tier hostname must change in the future, walk `git_repositories` with
a one-off script that calls GitHub's
`PATCH /repos/{owner}/{repo}/hooks/{hookId}` per row. We build the script
when the need arises, not before — a permanent admin endpoint to rewrite
every project's webhook URL is heavier than a once-in-years operation
warrants.

### Public-path security

Posture for the cloud delivery path, in order of how much we lean on each:

1. **HMAC validation** is the trust anchor. Already enforced
   (`services/webhook/verifier.go`); per-org rotation in `secrets.go`.
2. **Replay protection.** Already deduped by `X-GitHub-Delivery`
   (`services/webhook/deliveries.go`).
3. **IP allowlist.** GitHub publishes hook source ranges at
   `meta.hooks`. We do *not* enforce this at the gateway in phase 1 —
   HMAC alone is sufficient and an IP allowlist creates a periodic-refresh
   chore. Documented as an option, added only if a future incident
   motivates it.
4. **Rate limiting.** kgateway per-source-IP rate limit on the
   `/webhooks/github` route — start at 100 req/s per IP as a placeholder,
   tune after the first week of production metrics.
5. **Observability.** The BFF has no Prometheus infrastructure today.
   Phase 1 ships structured logs only — every receive path emits a slog
   line with `result=accepted|hmac_failed|dedup|unhandled_event` plus
   `tier`, `org`, and `event` fields, so a `result=hmac_failed` Loki
   query gives the same signal a counter would. When BFF gains Prometheus
   wiring (separate workstream), promote this to a
   `webhook_deliveries_total{tier,org,event,result}` counter.

### Config refactor — exhaustive list

| Surface | Change |
|---|---|
| `git-service/config/config_loader.go` | Read `GITHUB_WEBHOOK_DELIVERY_URL`. Hard rename — no soft alias. |
| `git-service/config/config.go` | Field `WebhookDeliveryURL` already named correctly; doc comment updates only. |
| `secret-references/github-webhook.yaml` (submodule) | Rename `secretKey` → `GITHUB_WEBHOOK_DELIVERY_URL`. Rename OpenBao `property` → `delivery_url`. |
| OpenBao seed (`scripts/lib/asdlc.sh`) | Re-seed `secret/apps/github-webhook delivery_url=...` in one shot. |
| Teardown (`scripts/teardown.sh`) | Update key reference. |
| `scripts/lib/env.sh _autogen_smee_url` | Continue auto-provisioning a smee channel on first local setup, written into `.env` as `GITHUB_WEBHOOK_DELIVERY_URL`. |
| `manifests/env-overlays/app-factory-git-service.yaml` | Use new var name. |
| `wso2cloud-deployment` per-tier branches | Set new SecretReference value to `<bff-public-host>/webhooks/github`. Add HTTPRoute. |
| `wso2cloud-deployment/app-factory-local/SMEE-CLIENT.md` | **Delete.** Superseded by this design; the relay no longer exists in the cluster. |

What does *not* change:

- `git-service/services/webhook_service.go` — URL-agnostic.
- `git-service/services/github_client.go RegisterWebhook` — URL-agnostic.
- `asdlc-service/services/webhook/*` — receives whatever GitHub delivers,
  validates HMAC the same way regardless of path.
- `DEPLOYMENT_TIER` — remains as the gate for phase 0/2 migration guards
  and the dev-tier platform-PAT seed. This design no longer references
  it; an implementer should not assume it can be retired.

### Manifest layout

```
deployments-v2/
├── manifests/env-overlays/
│   └── app-factory-git-service.yaml          # GITHUB_WEBHOOK_DELIVERY_URL placeholder
├── scripts/
│   └── webhook-relay.sh                      # NEW — host-side relay (port-fwd + smee-client)
└── wso2cloud-deployment/   (submodule, per-env branches)
    └── domains/platform/.../app-factory-api/
        └── httproute-webhooks.yaml           # NEW — public path for /webhooks/github
```

No `local-only/` directory. No new K8s manifests in the asdlc repo. The
local relay leaves no in-cluster footprint.

### Rollout

1. Land env-var hard rename in git-service **and** the OpenBao seed change
   in `scripts/lib/asdlc.sh` **and** the env-overlay update **as one PR**.
   The reader, the seed, and the overlay must move together — a partial
   landing boots git-service against an empty `WebhookDeliveryURL`. The
   same PR teaches `setup.sh` to detect a legacy `GITHUB_WEBHOOK_PROXY_URL=`
   line in `deployments-v2/.env` and rewrite it to the new key (or fail
   loudly with a one-line migration command); existing dev `.env` files
   must not silently break.
2. Add `deployments-v2/scripts/webhook-relay.sh`. Update setup.sh banner
   and `deployments-v2/README.md`. Delete
   `wso2cloud-deployment/app-factory-local/SMEE-CLIENT.md`.
3. Add `POST /internal/webhooks/reregister-all` to git-service.
4. For each cloud-tier branch in `wso2cloud-deployment`: register the
   tier's GitHub App, add the HTTPRoute, set the SecretReference value to
   the public BFF URL.
5. Update `CLAUDE.md`, `deployments-v2/README.md`, and
   `github-integration-evolution.md` to reflect the new model.
