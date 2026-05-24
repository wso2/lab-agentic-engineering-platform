---
name: thunder-authentication
description: How the platform's Thunder IDP is wired into SPAs that sign users in. Covers the callerIdentity.mode design field, the per-project Thunder OAuth client (BFF-owned — agent never sees client_id), the window._env_.THUNDER_* key set, and OIDC client wiring with oidc-client-ts. Pairs with react-webapp when the SPA wiring patterns apply. Apply on any project whose spec implies users sign in.
metadata:
  asdlc.version: "1"
---

# Thunder Authentication

## What this skill does

The platform delegates end-user authentication to Thunder (the WSO2
Identity Provider running on the cluster). This skill tells the
architect when to mark a web-app for sign-in, what the BFF provisions
behind the scenes, and how the SPA code reads OIDC config at runtime to
sign users in via Authorization Code + PKCE.

## Platform facts

- A per-project Thunder OAuth client is provisioned automatically when
  ANY component in the project declares `callerIdentity.mode: end-user`.
  The agent never sees the `client_id`, `client_secret`, or redirect
  URIs — they live in BFF code (`services/idp_service.go`).
- The redirect URI is computed by the BFF from the SPA's external URL.
- The BFF writes Thunder OIDC config into `window._env_` via the SPA's
  ReleaseBinding (`services/runtime_config_service.go:layerThunderKeys`).
  Authoritative keys (use these EXACT spellings — inventing one
  produces a `ReferenceError` at module load because the value is
  `undefined`):

  | Key | Meaning |
  |---|---|
  | `THUNDER_URL` | OIDC issuer / authority for `oidc-client-ts` |
  | `THUNDER_CLIENT_ID` | per-project Thunder OAuth client id |
  | `THUNDER_REDIRECT_URI` | absolute URL of this SPA's `/callback` route |
  | `THUNDER_SCOPES` | space-separated OIDC scopes (e.g. `openid profile email`) |
  | `THUNDER_AFTER_SIGN_IN_URL` | absolute URL to land on after sign-in (usually the SPA root) |

- The Thunder OIDC discovery endpoint is `<THUNDER_URL>/.well-known/openid-configuration`.
- Token endpoint: `<THUNDER_URL>/oauth2/token`. The SPA posts to it
  cross-origin — there is NO same-origin `/oidc/` proxy in nginx.
- Default Thunder admin user (dev clusters): `admin` / `admin` in the
  `Administrators` group. Real orgs add their own users via Thunder's
  admin console / SCIM.
- Switching IDPs (Asgardeo, custom) is a settings-page action against
  the org's `OrganizationIDPProfile` record — NOT a skill edit. The
  `THUNDER_*` keys are emitted unconditionally when `callerIdentity.mode:
  end-user` is set; a future PR honours the profile flavour. Until
  then, attaching an `asgardeo-authentication` custom skill produces
  code that *talks Asgardeo client semantics against a Thunder backend*
  — the OIDC handshake completes but Asgardeo-specific extensions
  don't apply.

## Recommended practice

### Architect

**Emitting `callerIdentity` is a HARD REQUIREMENT, not a minor omission.**
`callerIdentity` is a STRUCTURED design field the platform reads directly
— it is NOT satisfied by mentioning OIDC, sign-in, or Thunder in
`componentAgentInstructions`. `componentAgentInstructions` is for the
coding agent; `callerIdentity` is for the platform. Without the
structured field, NO per-project OAuth client is provisioned, NO
`THUNDER_*` keys land in `window._env_`, and the SPA deploys unable to
sign in. Treat a missing `callerIdentity` like a missing required schema
field — it produces a broken deployment.

- Whenever the spec implies users sign in (keywords: `login`, `sign in`,
  `user account`, `personal`, ...), mark the SPA component with:
  ```yaml
  callerIdentity:
    mode: end-user
  ```
- The protected backend it depends on must have
  `exposesAPI.auth: end-user-required` (see the `api-management` skill).
  The two are paired — without it the SPA logs in but its API calls all
  401.

Checklist before emitting `add_component` for a web-app:
  1. Does it depend on a service with `exposesAPI.auth: end-user-required`?
     → must have `callerIdentity.mode: end-user`.
  2. Does the spec contain "sign in", "login", "user account", or similar?
     → must have `callerIdentity.mode: end-user`.
  3. If either is yes and you didn't include the structured `callerIdentity`
     block, your output is incomplete.
- The web-app's `componentAgentInstructions` MUST say (verbatim or close):
  `OIDC Authorization Code + PKCE against the platform IDP using oidc-client-ts. Read OIDC + upstream URLs from window._env_.THUNDER_* / window._env_.<UPSTREAM>_URL — typed via src/env.ts. Attach Authorization: Bearer <access_token> to every API call. DO NOT write a .env file. DO NOT read environment variables at build time (no import.meta.env). DO NOT use envsubst, /etc/nginx/templates/, or any custom nginx entrypoint — stock nginx:alpine serves the static bundle + env-config.js.`
- Do NOT create a separate `auth` / `identity` / `login` /
  `session` / `user-service` component. Thunder owns token issuance;
  the API just reads `X-User-Id` (covered by `api-management`).
- Do NOT add `/auth/login`, `/auth/register`, `/auth/logout` endpoints
  to ANY backend service. Thunder issues tokens; the SPA initiates the
  redirect.

### Tech-lead — issue body bullets

For every web-app task whose component has `callerIdentity.mode: end-user`:

- Scope: "Implement OIDC Authorization Code + PKCE using
  `oidc-client-ts`, configured from `window._env_.THUNDER_*`. The
  platform writes OIDC + upstream URLs into `env-config.js` via the
  SPA's ReleaseBinding; the agent's `index.html` loads it synchronously
  before the bundle. Read values via the typed `src/env.ts` shim and
  throw at module top-level on missing keys — no `?? ''` fallback. Do
  NOT write a `.env` file. Do NOT use `import.meta.env.VITE_*`."
- Scope: "Attach `Authorization: Bearer <access_token>` to every
  `window._env_.API_BASE_URL` fetch. On 401, restart the login flow
  via `signIn()`. Do NOT write a `/login` form that POSTs credentials
  anywhere."
- Acceptance criteria: "Loading the webapp unauthenticated redirects to
  the OIDC authorize endpoint; after sign-in, the user lands back on
  the app with a token in sessionStorage; subsequent API calls carry
  `Authorization: Bearer <token>` and return per-user data; reloading
  the page keeps the user signed in."

### Coding agent — implementation

`src/env.ts` — typed read of `window._env_`. The `THUNDER_*` keys are
populated by the platform when the component's design has
`callerIdentity.mode: end-user`:

```ts
type Env = {
  API_BASE_URL: string;
  THUNDER_URL: string;
  THUNDER_CLIENT_ID: string;
  THUNDER_REDIRECT_URI: string;
  THUNDER_SCOPES: string;
  THUNDER_AFTER_SIGN_IN_URL: string;
};

declare global {
  interface Window { _env_: Env }
}

if (!window._env_) {
  throw new Error("window._env_ not set — /env-config.js failed to load.");
}

export const env: Env = window._env_;
```

`src/auth.ts` — `oidc-client-ts` wired to `env.THUNDER_*`:

```ts
import { UserManager, WebStorageStateStore } from "oidc-client-ts";
import { env } from "./env";

export const userManager = new UserManager({
  authority: env.THUNDER_URL,
  client_id: env.THUNDER_CLIENT_ID,
  redirect_uri: env.THUNDER_REDIRECT_URI,
  post_logout_redirect_uri: env.THUNDER_AFTER_SIGN_IN_URL,
  response_type: "code",
  scope: env.THUNDER_SCOPES,
  userStore: new WebStorageStateStore({ store: window.sessionStorage }),
  loadUserInfo: false,
});

export async function signIn()         { await userManager.signinRedirect(); }
export async function signOut()        { await userManager.signoutRedirect(); }
export async function handleCallback() { return userManager.signinRedirectCallback(); }

export async function getAccessToken(): Promise<string | null> {
  const user = await userManager.getUser();
  return user?.access_token ?? null;
}
```

Add a `/callback` route in your router that calls `handleCallback()`
once on mount and then navigates to `/`.

`src/api.ts` — attach `Authorization: Bearer <token>`; redirect on 401:

```ts
import { env } from "./env";
import { getAccessToken, signIn } from "./auth";

async function authHeaders(): Promise<HeadersInit> {
  const token = await getAccessToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export async function listTodos() {
  const res = await fetch(`${env.API_BASE_URL}/todos`, {
    headers: await authHeaders(),
  });
  if (res.status === 401) { await signIn(); return []; }
  return res.json();
}
```

### Don't

- ❌ Write a `/login` form that POSTs credentials to your API. Thunder
  owns token issuance.
- ❌ Invent `THUNDER_ISSUER` — the key is `THUNDER_URL`.
- ❌ Add a same-origin `/oidc/` proxy in nginx. The browser posts to
  `${env.THUNDER_URL}/oauth2/token` cross-origin.
- ❌ Hardcode the `client_id`. It changes per project; the BFF puts it in
  `window._env_.THUNDER_CLIENT_ID`.
- ❌ Add Thunder client provisioning code anywhere — the BFF does it on
  first dispatch when `callerIdentity.mode: end-user` is set.

### Common pitfalls

| Symptom | Cause | Fix |
|---|---|---|
| SPA loads, redirects to `undefined/oauth2/authorize` | Agent invented `THUNDER_ISSUER`; the real key is `THUNDER_URL` | Use `env.THUNDER_URL`. |
| After login, the callback shows "invalid redirect URI" | Agent overrode `redirect_uri` from a hardcoded value | Always use `env.THUNDER_REDIRECT_URI`; the BFF computed it. |
| Sign-in loops endlessly | `oidc-client-ts` written without `WebStorageStateStore({ store: sessionStorage })` | Use the constructor shown above; without it, state and PKCE verifier don't survive the redirect. |
| Callback route never resolves | Router intercepts `/callback` before mounting the handler | Make sure the route is registered + reachable AND calls `handleCallback()` once; the platform's `THUNDER_REDIRECT_URI` points at this path. |
