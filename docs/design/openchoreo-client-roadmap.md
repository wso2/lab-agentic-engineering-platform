# OpenChoreo Client — Future-Feature Roadmap

Captures the wrapper-surface decisions we deliberately *deferred* during the
oapi-codegen migration. Read alongside `openchoreo-client.md` (which describes
the current shape).

## Current scope

The wrapper at `asdlc-service/clients/openchoreo/` exposes four resource
clients: `NamespaceClient`, `ProjectClient`, `ComponentClient`,
`SecretRefClient`. Between them they cover every operation the BFF currently
calls — read namespaces/projects/components, create/upsert
workloads/releases/bindings, trigger builds, trigger the coding-agent,
ensure secret references.

## What gen exposes that we don't wrap (yet)

`clients/openchoreo/gen/client.gen.go` is the full OC OpenAPI surface — about
80 methods. We wrap roughly a third. The rest are first-class methods on
`*gen.ClientWithResponses` and require no codegen change to use; we just
haven't written the wrapper conversions yet.

**Namespace lifecycle (`CreateNamespace` / `UpdateNamespace` / `DeleteNamespace`)
is explicitly out of scope for this BFF.** OC namespaces ≡ organizations, and
tenant onboarding is owned by `platform-api-service` (hosted, driven by
Thunder's `notify_org_created` webhook) and `seed-admin-org.sh` (local). The
BFF authenticates as the end user and has no AuthZRole that permits namespace
creation; routing it here would also bypass Thunder OU minting and the
bootstrap content step. See `asdlc-service/controllers/organization_controller.go`.

Concrete features that will likely arrive once the BFF needs them:

| Capability | gen method(s) | Why we'll need it |
|---|---|---|
| **Trait management** | `AttachTraitsWithResponse`, `DetachTraitWithResponse`, `ListComponentTraitsWithResponse` | OTEL instrumentation, autoscaling, API-management — turning on OC traits per component |
| **Env-var mutation** | `UpdateComponentEnvVarsWithResponse`, `ReplaceComponentEnvVarsWithResponse`, `RemoveComponentEnvironmentVariablesWithResponse`, `UpdateReleaseBindingEnvVarsWithResponse` | Edit env vars from the console without re-running a full deploy |
| **Per-env release state** | `UpdateReleaseBindingStateWithResponse` (suspend/resume), `GetReleaseBindingK8sResourceTreeWithResponse` | Pause/resume a deployment, drill into pod state |
| **Schema introspection** | `GetComponentSchemaWithResponse`, `GetComponentTypeSchemaWithResponse`, `GetClusterComponentTypeSchemaWithResponse` | Render config forms in the console driven off the live ComponentType schema |
| **Patch / partial update** | `UpdateComponentWithResponse` (full PUT today, agent-manager has dedicated patch flows) | Edit display name / description after creation |
| **Environment CRUD** | `ListEnvironmentsWithResponse`, `GetEnvironmentWithResponse` | Multi-environment projects (dev/stage/prod) |
| **Endpoint introspection** | `GetReleaseBindingK8sResourceEventsWithResponse`, `GetReleaseBindingK8sResourceLogsWithResponse` | Stream pod events/logs into the console without going through the observability service |
| **GitSecret / secret CRUD (full)** | `CreateGitSecretWithResponse`, `ListGitSecretsWithResponse`, `DeleteGitSecretWithResponse`, `UpdateSecretReferenceWithResponse`, `DeleteSecretReferenceWithResponse` | Per-org git-secret rotation, vault-path migration |

## How to add a new wrapper method

Driven by a real caller — never speculatively. Per method, ~20 LOC pattern:

```go
//   1. Add the method to the resource client interface (NamespaceClient,
//      ProjectClient, ComponentClient, SecretRefClient).
//   2. Call the corresponding gen `*WithResponse` method.
//   3. Network err → fmt.Errorf("failed to <op>: %w", err) — wrap on the
//      transport-error path only, NOT on handleErrorResponse.
//   4. Non-success status → handleErrorResponse(resp.StatusCode(),
//      ErrorResponses{ JSON4xx/JSON5xx: resp.JSON4xx, ... }) — populate only
//      the fields the gen type actually exposes for that endpoint.
//   5. Convert gen.<Resource> → models.<Resource> via the existing helper
//      pattern (derefStr, derefTimeRFC3339, annotation, label).
//   6. Re-run `make gen-mocks` so the moq fake stays in sync.
```

Idempotency is a caller-driven decision:
- Default: surface 409 as `ErrConflict` (the gen-typed error path).
- "Ensure" semantics (`EnsureSecretReference`, `CreateComponent`'s
  refetch-on-409): only when the caller's contract is "make sure this exists
  and give me whichever copy ends up there". Justify in the doc comment.

## Anti-pattern: cargo-culting constants

Agent-manager's `clients/openchoreosvc/client/constants.go` has ~150 lines of
trait names, build-pack identifiers, deployment-status enums,
instrumentation-image registries, etc. **Do not copy these wholesale.**

Most are agent-manager domain concepts that don't exist in the BFF:
- `TraitOTELInstrumentation` / `TraitEnvInjection` — only relevant if/when
  the BFF starts attaching traits. Add the constant *and* the wrapper in the
  same change, driven by an actual caller.
- `ProvisioningInternal` / `ProvisioningExternal` — agent-manager's
  agent-deployment model; we don't have it.
- `DeploymentStatusActive` / `…InProgress` / `…Failed` — agent-manager
  tracks state mutations on ReleaseBinding; ours is read-only today.
- `WorkflowNameDocker` / `WorkflowNameBallerinaBuilpack` — the BFF doesn't
  pick the workflow at runtime; the ClusterComponentType pins it.

Rule of thumb: **a constant exists when at least one wrapper method
references it.** Dead constants rot silently and lie to future readers about
what the BFF supports.

## When the OC spec bumps

`make gen-oc-client` regenerates `clients/openchoreo/gen/*.gen.go` against
the pinned spec version in `asdlc-service/Makefile` (`OC_SPEC_VERSION`).
Workflow:

1. Bump `OC_SPEC_VERSION`.
2. `make gen-oc-client` — overwrites the two `gen` files.
3. `go build ./...` — typed-gen makes every breaking change a compile error
   at the wrapper site. Backwards-compatible bumps compile unchanged.
4. Fix wrapper conversions at each error.
5. `make gen-mocks` — re-stamp the moq fakes against the (possibly changed)
   interfaces.
6. `make gen-oc-client-check` — CI gate that the committed `gen/` matches the
   pinned version.

Two failure modes codegen *can't* catch (still need integration tests):
- **Dynamic-map shape changes** — `WorkflowRun.Spec.Workflow.Parameters`,
  `ComponentRelease.Spec.ComponentType`, `ComponentRelease.Spec.Workload`
  are all `map[string]interface{}` on the gen side. If OC renames a nested
  key (e.g. `parameters.repository.revision.commit → parameters.repo.rev.sha`),
  the wrapper compiles but produces runtime 400s.
- **Semantic drift** — status codes flipping (201 → 200), fields previously
  always-nil starting to populate, label-selector matching semantics
  changing. Codegen sees no diff; only the live deploy path does.

Mitigation: keep at least one E2E smoke test that exercises
project → component → workload → build → deploy against a real cluster.
