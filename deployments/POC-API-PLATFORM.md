# POC log — WSO2 API Platform + Thunder JWT on App Factory v1 (`deployments/`)

Branch: `poc-api-platform` (tracking `upstream/design-revamp`)

> **Reading order**: this doc captures what was *built and learned* during the POC.
> For the *production implementation plan* (UI, agents, BFF, build-workflow integration, multi-IDP), see [`docs/design/api-platform-integration.md`](../docs/design/api-platform-integration.md).
> Start there if you're picking up the work post-compaction.

## Goal

Prove the WSO2 API Platform gateway + the `api-configuration` ClusterTrait + Thunder JWT validation work end-to-end on the `deployments/` k3d cluster. Two hand-rolled hello-world Components: one authenticated, one open. No AI, no BFF code, no console UI.

## Truth table we want to pass

|                | with valid token | without token |
|----------------|------------------|---------------|
| **protected**  | 200              | 401 at gateway|
| **public**     | 200 (token ignored) | 200        |

Both Components go through the AP router (same `api-configuration` trait attached), differing only in `traitEnvironmentConfigs.<inst>.jwtAuth.enabled`.

## Scope decisions (locked)

| Decision | Choice |
|---|---|
| IDP scope (v1) | Platform IDP only (Thunder). BYO-IDP shape carried in CRs but parked. |
| OAuth client registration | Org-level (deferred — POC uses a single hand-created Thunder client). |
| Toggle source-of-truth (future) | `design.md` frontmatter. POC has no design.md — config lives directly in the ReleaseBinding. |
| Spec emission ownership (future) | Build workflow's `generate-workload-cr` step. POC bypasses with hand-authored Workloads. |
| Image | Stock `mendhak/http-https-echo:35`. No build, no Workflow, no GitHub repo. |
| Install location | Fold AP install into `setup-prerequisites.sh`. Hello-worlds in `deployments/manifests/`. |
| Trait source | Vendor `deployments-v2/wso2cloud-deployment/.../traits/api-configuration.yaml` into `deployments/manifests/`. |

## Architecture in one diagram

```
browser/curl
   │
   ▼
Kgateway HTTPRoute (path-prefix /<comp>-<endpoint>, patched by trait → AP backend)
   │
   ▼
kgateway Backend (Static) → api-platform-default-gateway-gateway-runtime.openchoreo-data-plane:8080
   │
   ▼
AP gateway-router (Envoy)
   │  matches RestApi.context = /<env>-<ns>-<comp>-<endpoint>
   │  policy_engine (ext_proc) — jwt-auth v0 against Thunder JWKS (PlatformIDPKeyManager)
   ▼
Component Service → Pod (mendhak/http-https-echo:35)
```

## Pieces being added to `deployments/`

| Layer | Artifact | Source |
|---|---|---|
| Helm | `gateway-operator` v0.4.0 from `oci://ghcr.io/wso2/api-platform/helm-charts` | vendored into setup-prerequisites.sh |
| ConfigMap | `api-platform-operator-gateway-values` in `openchoreo-data-plane` | `deployments/manifests/api-platform/gateway-config.yaml` (copy of canonical) |
| RBAC | `wso2-api-platform-gateway-module` ClusterRole + Binding for `cluster-agent-dataplane` SA | `deployments/manifests/api-platform/rbac.yaml` |
| APIGateway CR | `api-platform-default` in `openchoreo-data-plane` | `deployments/manifests/api-platform/api-gateway.yaml` |
| ClusterTrait | `api-configuration` (canonical wso2cloud shape) | `deployments/manifests/api-platform/api-configuration-trait.yaml` |
| ClusterComponentType patch | `service` gains `allowedTraits: [api-configuration]` | inline in `setup-asdlc.sh` |
| Project CR | `poc-api-platform` | `deployments/manifests/poc-api-platform/00-project.yaml` |
| Component+Workload+RB ×2 | `poc-public`, `poc-protected` | `deployments/manifests/poc-api-platform/{10,20}-*.yaml` |
| Thunder OAuth client | `poc-api-platform-client` (client_credentials) | `deployments/scripts/setup-thunder-client.sh` |
| Verify script | mints token, runs 4 curls, asserts truth table | `deployments/scripts/verify-api-platform.sh` |

## Findings as they land

### ✅ Confirmed before any apply

1. **gateway-config Thunder keymanager exists in the canonical file** — `jwtauth_v0.keymanagers[0]` with `issuer: http://thunder.openchoreo.localhost:8080` and `jwks.remote.uri: http://thunder-service.thunder.svc.cluster.local:8090/oauth2/jwks`. POC reuses it as-is.

2. **Canonical trait emits `jwt-auth` policy `version: v0`** — matches the keymanager scope in gateway-config. No `v1` mismatch concern.

3. **Hardcoded AP router service name in trait** — `api-platform-default-gateway-gateway-runtime.openchoreo-data-plane:8080`. APIGateway must be named `api-platform-default` and live in `openchoreo-data-plane`. POC matches.

4. **BYOI Workload pattern is supported** — `platform-idp` example (`deployments-v2/.../platform-idp/`) shows a Component without a `workflow:` block, paired with a standalone Workload CR. POC uses the same pattern.

5. **Workload endpoints schema** (from OC CRD `workloads.yaml:222-285`):
   ```yaml
   spec:
     endpoints:
       <name>:
         port: <required>
         targetPort: <optional>
         type: HTTP | gRPC | GraphQL | Websocket | TCP | UDP   # required
         visibility: [external | internal | namespace]          # set; "project" is implicit
         basePath: /                                            # optional
   ```
   ⚠️ `service` ClusterComponentType's HTTPRoute CEL filter (`setup-asdlc.sh:164`) lists `["HTTP", "REST", "GraphQL", "Websocket"]` — but the CRD doesn't allow `REST`. POC uses `HTTP`.

6. **`service` ClusterComponentType today has NO `allowedTraits`** (`setup-asdlc.sh:88-92`). POC must patch this in to allow `api-configuration`.

### 🟡 To verify during apply

- AP operator helm install succeeds against k3d's containerd (image pulls from ghcr.io).
- `cluster-agent-dataplane` ServiceAccount exists at the time RBAC binds it (created by OC install).
- `openchoreo-data-plane` namespace exists when AP install runs (created by OC install, not by setup-prerequisites.sh today).
- Gateway-controller pod becomes Ready (depends on cert-manager Issuer `selfsigned-issuer`).
- AP gateway-router pod can resolve `thunder-service.thunder.svc.cluster.local`.
- Thunder admin API (`POST /applications`) accepts `client_credentials`-only payload.
- The patched HTTPRoute's URL-rewrite actually changes — needs `kubectl get httproute -o yaml` after apply.

### 🔴 Known gotchas / open questions

- **Ordering: AP install needs `openchoreo-data-plane` namespace**. Today `setup-prerequisites.sh` runs BEFORE `setup-openchoreo.sh` (which creates that namespace). Options:
  1. Move AP install to AFTER OC install (new step in `setup.sh` between `setup-openchoreo.sh` and `setup-asdlc.sh`).
  2. Create the namespace explicitly at the top of the AP install block.
  Picking option 2 — it keeps the user's directive ("fold into `setup-prerequisites.sh`") and only costs one `kubectl create namespace`.

- **RBAC depends on `cluster-agent-dataplane` SA** which is created by OC. Applying the ClusterRoleBinding before the SA exists is allowed by k8s (subjects don't have to exist at bind time), but the trait reconciler that uses it won't work until OC is up. POC applies RBAC at setup-prerequisites and verifies behaviour at verify-time.

- **Trait `allowedTraits` patch** — modifying the existing ClusterComponentType inline in `setup-asdlc.sh`. If the operator already created it without `allowedTraits`, `kubectl apply` should reconcile to the new spec. Worth verifying.

- **CORS** — the `service` ClusterComponentType's HTTPRoute does NOT add a Gateway-API CORS filter (different from the wso2cloud overlay). No double-CORS risk. CORS via the trait's `cors` policy is the only layer.

- **No `issuers` / `audience` params on the v0 jwt-auth policy** — the trait can't constrain which issuer or audience is accepted. Any token from any cluster keymanager passes. Acceptable for POC (single keymanager). Same gap flagged earlier for BYO-IDP.

- **URL shape post-trait-patch** — `/<env>-<ns>-<comp>-<endpoint>` (`api-configuration.yaml:229,267`). The discovered curl URL will be e.g. `http://development-default.<gateway-host>/<comp>-<endpoint>/...` which kgateway then rewrites to `/development-default-poc-public-http/...` before sending to the AP router. Need to discover the actual gateway hostname after apply.

## Run log

### ✅ Step 6 — AP operator install (setup-prerequisites.sh)

- Helm install of `gateway-operator` v0.4.0 from `oci://ghcr.io/wso2/api-platform/helm-charts/gateway-operator` succeeded.
- CRDs installed by the chart: `apigateways.gateway.api-platform.wso2.com`, `restapis.gateway.api-platform.wso2.com` — both `v1alpha1`. ✓ Matches what the canonical trait emits.
- `api-platform-operator-gateway-operator` Deployment came up 1/1 in ~40s.
- `gateway-config` ConfigMap (`api-platform-operator-gateway-values`), RBAC ClusterRole+Binding, and APIGateway CR (`api-platform-default`) all applied without errors.
- APIGateway reconciled → operator created two Deployments:
  - `api-platform-default-gateway-controller` — Ready after ~75s (image pull from ghcr.io was the slow bit).
  - `api-platform-default-gateway-gateway-runtime` — Ready after ~35s.
- ⚠️ **Finding: no separate `policy-engine` Deployment in chart v0.9.0.** The `policy-engine` config in `gateway-config.yaml` (`policy_engine:` block at lines 126–152) is still consumed, but the process runs **inside the gateway-runtime pod**. The Service exposes its admin/metrics ports (9002, 9003) on the same `api-platform-default-gateway-gateway-runtime` Service. The trait's hardcoded backend host (`...-gateway-gateway-runtime:8080`) still works.
- ⚠️ **Finding: chart NOTES.txt mentions `GatewayConfiguration` + `APIConfiguration` CRDs in group `api.api-platform.wso2.com`** — those are documentation boilerplate, not installed in this version. Ignore.

### ✅ Step 7 — `setup-asdlc.sh` (trait + allowedTraits)

- `clustercomponenttype.openchoreo.dev/service configured` — `kubectl apply` updated the in-place CRD with the new `allowedTraits: [api-configuration]` block. No restart needed.
- `clustertrait.openchoreo.dev/api-configuration created` — OC webhook accepted the trait on first try (no retry needed). `apply_with_retry` works.

### ✅ Step 8 — POC manifests applied + reconciled

After fixing the first gotcha (see below), all 7 manifests applied. The Component reconciler produced:
- 2 Deployments + 2 Services (Pods Running 1/1)
- 2 HTTPRoutes (Gateway-API, hostname `development-default.openchoreoapis.localhost`)
- 2 RestApis (group `gateway.api-platform.wso2.com/v1alpha1`)
- 2 kgateway Backends (`Static`, `Accepted: True`)

### ✅ Step 9 — Truth table (4/4 PASS)

```
public + no token       → 200 (expect 200) ✓
public + valid token    → 200 (expect 200) ✓
protected + no token    → 401 (expect 401) ✓
protected + valid token → 200 (expect 200) ✓
```

- The 401 response body — `{"error":"Unauthorized","message":"Authentication failed."}` — matches the `errormessage` configured in `gateway-config.yaml:124`. **Confirms the rejection comes from the AP policy-engine, not the backend.**
- On the 200 path, the backend echo (`mendhak/http-https-echo`) shows the full `Authorization: Bearer …` header arrived, plus `x-envoy-original-path: /development-default-poc-protected-http/` (proof of the trait's URL-rewrite patch firing) and `x-envoy-original-host: development-default.openchoreoapis.localhost:19080` (kgateway's original host header).
- Upstream `host` header on the backend is `poc-protected.dp-default-poc-api-platf-development-5c9de501`, confirming AP router → in-cluster Service via Service DNS.

## Gotchas hit during bring-up — for future reference

### 🔴 Gotcha 1: `Component.spec.componentType.name` requires the workloadType prefix

First apply failed:
```
The Component "poc-public" is invalid: spec.componentType.name: Invalid value: "service":
spec.componentType.name in body should match '^(deployment|statefulset|cronjob|job|proxy)/[a-z0-9]([-a-z0-9]*[a-z0-9])?$'
```

The ClusterComponentType's `metadata.name` is bare (`service`), but Components must reference it as `deployment/service`. Fixed by updating both Component YAMLs.

### 🔴 Gotcha 2: Reconciled resources land in a per-environment data-plane namespace, not the Component's namespace

The verify script was looking for `Deployment/poc-public` in `default` (where the Component lives) and timing out. Resources actually land in `dp-default-poc-api-platf-development-5c9de501` — a hash-suffixed namespace OC creates per `Environment` × `Project`. Discovered via `ReleaseBinding.status.endpoints[].serviceURL.host`.

**Implication for any future tooling that inspects rendered resources**: read `ReleaseBinding.status.endpoints[].serviceURL.host` to discover the data-plane namespace, OR list across all namespaces.

The trait-generated `RestApi` names are also longer than what the trait template suggests (`metadata.name` resolves to the ComponentRelease name, not the Component name):
- expected: `poc-public-http`
- actual: `poc-public-development-1636ea0e-http`

### 🔴 Gotcha 3: Thunder admin API isn't accessible without a Bearer token

`setup-thunder-client.sh` failed with `401 unauthorized` even from inside the Thunder pod calling `localhost:8090`. Thunder v0.34 enforces auth on admin endpoints; bootstrap scripts that ship with the image presumably run during a pre-auth init phase. Getting an admin token from outside that path is non-trivial (no documented admin token endpoint in this env).

**POC workaround**: use an already-bootstrapped confidential client (`asdlc-api-client` / `asdlc-api-client-secret`) to mint tokens. The trait's `jwt-auth v0` policy emits no `audience` or `issuers` filter, so any token signed by the cluster's configured keymanager passes — regardless of which client minted it. This is the same gap flagged earlier: the canonical trait can't constrain accepted audiences, which makes it both easier for POC and the reason BYO-IDP needs the upstream trait extension.

**Production impact**: `setup-thunder-client.sh` as written is unusable. Either (a) wait for Thunder to expose a documented admin token mechanism, (b) drive client creation via Helm bootstrap rows in `values-thunder.yaml` (the existing pattern — all 7 platform clients live there), or (c) shell into Thunder during its early bootstrap phase. None are ideal for an automated POC verifier.

### 🟡 Gotcha 4: No separate `policy-engine` Deployment in chart v0.9.0

The `gateway-operator` chart v0.4.0 (gateway v0.9.0) deploys only:
- `<APIGateway-name>-gateway-controller`
- `<APIGateway-name>-gateway-gateway-runtime`

The policy-engine config still applies, but it runs inside the gateway-runtime pod (ports 9002/9003 are exposed on the runtime Service). The hardcoded backend target in the trait still works.

### 🔴 Gotcha 5: AP router 404s without a trailing slash on the context path

The `ReleaseBinding.status.endpoints[0].externalURLs.http.path` is `/poc-public-http` (no trailing slash). Curling that path returns 404. Adding a trailing slash returns the expected 200/401.

What's happening: kgateway HTTPRoute matches `PathPrefix: /poc-public-http`, rewrites to `/development-default-poc-public-http`. The AP router's `RestApi.context` is `/development-default-poc-public-http`. A bare context match (no trailing slash, no sub-path) appears to miss the AP router's routing table; only path WITH trailing slash (or a sub-path) lands. This is the AP router's behaviour — not kgateway's. **Anything consuming OC endpoint URLs at the gateway layer must append `/` to the discovered `externalURLs.http.path`.**

Fixed in `verify-api-platform.sh` by trimming + appending the slash before curling.

### 🟡 Gotcha 6: Chart NOTES advertises CRDs that don't exist

Helm output references `GatewayConfiguration` / `APIConfiguration` in group `api.api-platform.wso2.com/v1alpha1`. These are not installed — chart boilerplate. The real CRDs are `APIGateway` / `RestApi` in `gateway.api-platform.wso2.com/v1alpha1`.

### 🟢 Non-issues / good news

- `kubectl apply` on an existing ClusterComponentType to add `allowedTraits` works cleanly — `configured`, no restart, no controller burp.
- The `apply_with_retry` helper in `setup-asdlc.sh` is unnecessary for this — the OC webhook was up when we applied.
- Component → ComponentRelease → RenderedRelease pipeline took ~25s end-to-end from Component apply to Pod Ready.
- AP operator + gateway-runtime image pulls from ghcr.io took ~75s on first pull; subsequent re-runs skipped.

## What the POC *did not* prove

- **No audience filtering** at the gateway — any Thunder-signed token passes. To prove per-app audience scoping, the canonical trait needs the upstream PR adding `issuers` + `audience` params (see `[[reference_agent_manager_api_platform_wiring]]`).
- **No rate-limit / add-headers policies** tested. The trait emits them under `environmentConfigs.{rateLimit,addHeaders}` but we left both `enabled: false`.
- **No CORS preflight** tested — only direct curl. Worth a quick browser test from the console if we keep CORS on by default.
- **No upstream MTLS** — gateway → backend is plain HTTP via Service DNS.
- **No App Factory integration** — the BFF still has no idea this exists. The next step is wiring `design.md` frontmatter → BFF emits these YAMLs at build-workflow time. That's the actual product change; this POC just proved the substrate.

## Updated scripts after POC

- `verify-api-platform.sh` — needs an update to:
  - Look up the data-plane namespace dynamically (`kubectl get releasebinding ... -o jsonpath='{.status.endpoints[0].serviceURL.host}'`).
  - Default to using `asdlc-api-client` instead of `setup-thunder-client.sh`.
- `setup-thunder-client.sh` — leave for now but flag as broken in the docstring. Either delete or rework once we understand the Thunder admin auth path.

## Net result

The platform substrate works exactly as designed. App Factory can adopt this with confidence:

1. Install the AP operator (folded into `setup-prerequisites.sh`).
2. Apply the canonical trait + add to `allowedTraits` (folded into `setup-asdlc.sh`).
3. Components opt-in by including the `api-configuration` trait + setting `jwtAuth.enabled: true` in the ReleaseBinding's `traitEnvironmentConfigs`.

The remaining work is:
- Decide how to drive Thunder client registration from App Factory (likely: add to `values-thunder.yaml` bootstrap or wait for Thunder admin token story).
- Land the upstream trait extension to expose `issuers` + `audience` to enable BYO-IDP.
- Wire `design.md` frontmatter → BFF spec emission (the v1 we discussed before the POC).

## Reference paths

- AP install vendor source: `deployments-v2/wso2cloud-deployment/wso2cloud-local/init/api-platform/{helm-release.yaml,gateway-config.yaml,rbac.yaml}` + `init/api-platform-gateway/api-gateway.yaml`
- Canonical Trait: `deployments-v2/wso2cloud-deployment/wso2cloud-local/domains/platform/namespaces/wso2cloud/definitions/traits/api-configuration.yaml`
- BYOI Workload example: `deployments-v2/wso2cloud-deployment/wso2cloud-local/domains/developers/namespaces/wso2cloud/projects/core/components/platform-idp/`
- ComponentType CEL: `deployments/scripts/setup-asdlc.sh:83-195`
- Workload CRD: `/Users/wso2/openchoreo-sources/openchoreo/config/crd/bases/openchoreo.dev_workloads.yaml`
