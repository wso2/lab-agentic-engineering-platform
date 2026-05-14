# Build Auth-Failure Classification

Filed as the §11.2 prerequisite of `task-execution-progress.md`. Documents
a latent bug in the build watcher and the replacement classifier that
must land before the §5.2 watcher work in that design can rely on
`Tasks[]` for failure-cause attribution.

---

## 1. Bug summary

`asdlc-service`'s OC client and webhook layer both reference an `Outputs`
map on `WorkflowRunTask` that **does not exist on the upstream OC CRD**.
The classifier that depends on it has been a silent no-op since day 1.

### 1.1 The phantom field

`asdlc-service/clients/openchoreo/oc_types.go:166-178` defines the
client-side projection:

```go
type ocParameter struct {
    Name  string `json:"name"`
    Value string `json:"value,omitempty"`
}

type ocTaskOutputs struct {
    Parameters []ocParameter `json:"parameters,omitempty"`
}

type ocTask struct {
    Name    string         `json:"name"`
    Outputs *ocTaskOutputs `json:"outputs,omitempty"`
}
```

The flat model at `asdlc-service/models/component.go:115-118` mirrors the
same shape:

```go
type WorkflowRunTask struct {
    Name    string            `json:"name"`
    Outputs map[string]string `json:"outputs,omitempty"`
}
```

Populator: `asdlc-service/clients/openchoreo/component_client.go:179-202`
flattens `task.Outputs.Parameters` into the map, then opportunistically
extracts `image` (from the `publish-image` step) and `git-revision` (from
`checkout-source`) onto the run.

### 1.2 What OC actually returns

`/Users/wso2/openchoreo-sources/openchoreo/api/v1alpha1/workflowrun_types.go:80-109`
defines the canonical `WorkflowTask`:

```go
type WorkflowTask struct {
    Name        string       `json:"name"`             // Argo node displayName
    Phase       string       `json:"phase,omitempty"`  // Pending|Running|Succeeded|Failed|Skipped|Error
    StartedAt   *metav1.Time `json:"startedAt,omitempty"`
    CompletedAt *metav1.Time `json:"completedAt,omitempty"`
    Message     string       `json:"message,omitempty"`
}
```

There is **no `Outputs` field** on the CRD. The OC API server never emits
one. Every `task.Outputs` deref in the asdlc-service codebase reads `nil`,
and every `WorkflowRunTask.Outputs` map is empty.

### 1.3 Downstream impact

`asdlc-service/services/webhook/build_watcher.go:229-243` is the only
consumer:

```go
func isGitCloneAuthFailure(run *models.WorkflowRun) bool {
    for _, task := range run.Tasks {
        for _, val := range task.Outputs {  // always empty
            for _, marker := range authFailureMarkers {
                if strings.Contains(val, marker) {
                    return true
                }
            }
        }
    }
    return false
}
```

Returns `false` for every failed run. As a result:

- `classifyRun` (`build_watcher.go:212-227`) routes every git-clone
  auth failure straight to `TaskEventBuildFailed` instead of the
  retry path.
- `WorkflowRunService.RetryAuthFailedBuild`
  (`build_watcher.go:160`) is never invoked.
- `TaskEventBuildAuthRetryExhausted` (`services/task_state.go:28`) is
  never emitted.
- `ComponentTask.Cause = "build.auth_retry_exceeded"`
  (`task_state.go:46`) never appears in the DB.
- `ComponentTask.BuildAuthRetryCount` is never incremented.

Convenience extracts (`image`, `git-revision`) at
`component_client.go:191-202` are also broken — the watcher just doesn't
depend on them today, so the absence is invisible.

The `build_watcher_test.go` suite (the only place this code looks like
it works) constructs `WorkflowRunTask{Outputs: ...}` synthetically, so
unit tests stay green even though production data is always empty. The
divergence between fixture and reality is the reason this bug went
unnoticed.

---

## 2. Failure-cause taxonomy

Every `TaskEvent` defined in `services/task_state.go:13-33` and its
`EventCause` mapping (`task_state.go:39-61`):

| TaskEvent | Terminal? | Cause string written | Source | Status today |
|---|---|---|---|---|
| `dispatch.success` | no (→ in_progress) | (none) | dispatcher | works |
| `pr.ready_for_review` | no (→ ready_for_review) | (none) | webhook | works |
| `pr.merged` | no (→ merged) | (none) | webhook | works |
| `pr.rejected` | yes | `pr.rejected` | webhook | works |
| `push.matched` | no (→ building) | (none) | webhook | works |
| `build.succeeded` | yes | `build.deployed` | build_watcher | works |
| `build.failed` | yes | `build.failed` | build_watcher | works |
| `org.disconnected` | yes | `org.disconnected` (overridable) | OrgDisconnectService | works |
| `repo.unselected` | yes | `repo.unselected` | reach reconciler | works |
| **`build.auth_retry_exceeded`** | yes | `build.auth_retry_exceeded` | build_watcher | **dead — depends on phantom Outputs** |
| `coding_agent.failed` | yes | `coding_agent.failed` | coding_agent_watcher | works |

Only `build.auth_retry_exceeded` is downstream of the phantom field. Every
other event source already uses signals the OC CRD actually exposes
(WorkflowRun-level `status.conditions[type=WorkflowCompleted]`,
`status.conditions[type=WorkflowRunning]` — see
`component_client.go:167-176`).

---

## 3. Replacement classifier

Switch `isGitCloneAuthFailure` from "any task's Outputs contains a
marker" to "the `checkout-source` task is in a terminal-failure phase
AND its `Message` contains a marker". Use the existing
`authFailureMarkers` substring list verbatim.

```go
// Replacement — drop-in for build_watcher.go:232-243.
func isGitCloneAuthFailure(run *models.WorkflowRun) bool {
    for _, task := range run.Tasks {
        if task.Name != "checkout-source" {
            continue
        }
        if task.Phase != "Failed" && task.Phase != "Error" {
            continue
        }
        for _, marker := range authFailureMarkers {
            if strings.Contains(task.Message, marker) {
                return true
            }
        }
        // Message can be empty when the controller didn't capture stderr
        // (e.g. exit-code-only failure). Fall back to OC `/logs?task=`.
        return matchesAuthFailureLogs(run, task)
    }
    return false
}
```

Required CRD-shape changes (separate from this design — they belong to
the §5.2 fix):

1. **`oc_types.go:175-178`** — extend `ocTask` with `Phase`, `Message`,
   `StartedAt`, `CompletedAt`. Drop `Outputs` (or keep it `omitempty`
   for transitional release; see §4).
2. **`models/component.go:115-118`** — extend `WorkflowRunTask` with
   `Phase string`, `Message string`, `StartedAt *time.Time`,
   `CompletedAt *time.Time`. Drop `Outputs`.
3. **`component_client.go:179-202`** — populate the new fields. The
   `image` / `git-revision` extracts go away with the field; the
   watcher does not consume them today, so this is safe deletion.
4. **`build_watcher_test.go`** — rewrite the auth-failure fixtures to
   use `Phase: "Failed"` + `Message:` instead of `Outputs:`.
5. **Webhook projector docstring** — note that `Tasks[].Outputs` was
   never populated; this clears the lie in the model comment at
   `models/component.go:111-114`.

### 3.1 `/logs?task=` fallback

`task.Message` can be empty when the Argo controller doesn't capture the
failing-step stderr (typically when the container exits non-zero without
writing to stderr, or when the failure is at the controller layer e.g.
image pull). For those cases, fetch the failing step's stdout via the
OC gateway:

```
GET {ocApi}/api/v1/namespaces/{ns}/workflowruns/{run}/logs?task=checkout-source&sinceSeconds=0
```

This endpoint is the same one OC's UI uses; capped at 10MB per call.
Apply the same `authFailureMarkers` substring scan to the body.

`matchesAuthFailureLogs` is a thin client method on the OC component
client, returning `true` on substring match and `false` on
no-match / 404 / call failure. **Conservative on error**: return
`false` if `/logs` errors out — better to mis-classify as a regular
failure than to spin the retry budget on transient OC API hiccups.

---

## 4. Migration

Don't cut over in one step. Run **dual classification** for one release:

1. Keep `isGitCloneAuthFailure` (the broken Outputs-iterating version)
   under a new name `isGitCloneAuthFailureLegacy`.
2. Add the new `isGitCloneAuthFailureV2` (the Phase + Message
   implementation).
3. In `classifyRun`, call both. When they disagree, log:
   ```
   slog.Info("build_watcher.classify_diverged",
     "run", run.Name, "legacy", legacyResult, "v2", v2Result,
     "tasks", run.Tasks)
   ```
   Use `v2Result` for the actual decision.
4. Cut over (delete the legacy path) once the divergence log stays empty
   across a representative set of failing builds — meaning a week of
   real traffic with at least one observed git-clone auth failure that
   the v2 classifier handled correctly.

The dual-classify code is throwaway; budget ≤ 50 lines of `build_watcher.go`
and one log statement.

Because the legacy classifier is currently always `false`, the migration
is biased safely: the new classifier can only **add** retries, never
remove them. The week of dual-classify is a smoke check on the new path,
not a safety net for the old one.

---

## 5. Step-name verification action item

The replacement classifier hard-codes `"checkout-source"` for the failing
step. This must be the actual Argo node `displayName` the
`app-factory-coding-agent` ClusterWorkflow emits, **not** the templateName.

Per `workflowrun_types.go:80-87`:

```
// Name is the name of the task/step.
// For Argo Workflows, this corresponds to the node's displayName (or parsed
// node name from the node name pattern "workflow-name[N].step-name"), not
// the templateName, since workflows now use ClusterWorkflowTemplates with
// templateRef instead of inline templates.
```

**Verified for the build path** — both build ClusterWorkflows in the
submodule (`dockerfile-builder.yaml:141`,
`paketo-buildpacks-builder.yaml:111`) declare the step as
`name: checkout-source`. The Argo `displayName` defaults to the step
name, so `Tasks[].Name == "checkout-source"` should hold.

**Not yet verified for the coding-agent path** — `app-factory-coding-agent`
is a different ClusterWorkflow (`deployments-v2/wso2cloud-deployment/.../cluster-workflows/app-factory-coding-agent.yaml`).
If the coding-agent runner does its own clone (not via a `checkout-source`
step), the auth-failure classifier doesn't apply to it directly; instead
auth failures bubble up via the runner's exit code → `coding_agent.failed`
event. Confirm by inspecting the ClusterWorkflow once the obs-plane is
live: dispatch one task (real or test), then

```
kubectl get workflowrun <name> -n openchoreo-workflow-plane \
  -o jsonpath='{.status.tasks[*].name}'
```

and confirm the literal step names. Update the matcher if anything
differs from `checkout-source`.

---

## 6. Out of scope

- The §5.2 watcher implementation that consumes this design.
- Auth-failure detection on the coding-agent runner itself
  (handled by the runner's exit-code → `coding_agent.failed` path,
  not by `isGitCloneAuthFailure`).
- Re-architecting the retry budget. The retry mechanism in
  `WorkflowRunService.RetryAuthFailedBuild` and the `BuildAuthRetryCount`
  column already exist and work correctly — this design only restores
  the classifier signal that gates them.
