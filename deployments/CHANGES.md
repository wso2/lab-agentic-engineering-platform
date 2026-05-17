# v1 deployments — restored and verified end-to-end (2026-05-17)

## Summary

The v1 `deployments/` setup (pure OpenChoreo + Docker Compose) was outdated against the current source — references to long-removed services (`database-service`, `collab-server`), the now-replaced long-lived `remote-worker`, and missing env vars (`AGENT_GIT_SERVICE_URL`, `AGENT_PLATFORM_URL`, `OBSERVER_URL`, `ANTHROPIC_PLATFORM_KEY`, …) made `docker compose up` produce a stack that couldn't serve the UI flow end-to-end.

I rewrote the v1 setup to mirror agent-manager's docker-compose pattern (`/Users/wso2/repos/agent-manager/deployments/`) as closely as possible while honouring App Factory's heavier needs (git-service writes K8s Secrets into `workflows-*` namespaces, coding-agent is dispatched as a one-shot ClusterWorkflow pod), then ran the full flow from "Create Project → Generate Requirements → Architecture → Tasks → Code → Build → Deploy" in a browser to confirm.

## E2E result

```
✅ admin / admin login
✅ Create project "hello-api" — git-service provisions repo via PAT
✅ Generate requirements (chat stream visible, files saved + tagged v1)
✅ Generate architecture (design.md + cell diagram + tagged v1-1)
✅ Generate tasks → GitHub issue #1 opened on the repo
✅ Dispatch via Remote Agents → one-shot Argo pod runs the Claude Agent SDK
✅ Agent writes Express service, commits, pushes, opens PR #2
✅ Merge PR → BFF webhook → dockerfile-builder ClusterWorkflow dispatched
✅ Build pipeline: checkout → containerfile-build → publish-image → generate-workload-cr
✅ Deployment running in dp-default-hello-api-development-* namespace
✅ Public endpoint reachable:
     curl -H "Host: development-default.openchoreoapis.localhost" \
          http://localhost:19080/hello-api-hello-api-http/hello
     → {"message":"Hello, World!"}
     curl -H "Host: development-default.openchoreoapis.localhost" \
          http://localhost:19080/hello-api-hello-api-http/health
     → {"status":"ok"}
```

## Files changed

| File | Change |
|---|---|
| `deployments/docker-compose.yml` | Rewritten: removed `mysql/database-service`, `collab-server`, long-lived `remote-worker`. Updated env vars to match current code. Mounts the host kubeconfig + Task signing PEM into git-service / asdlc-api. |
| `deployments/scripts/setup-asdlc.sh` | Installs `dockerfile-builder` + **new** `app-factory-coding-agent` ClusterWorkflow. Creates `workflows-default` namespace. Binds Thunder's `Administrators` group → OC `admin` role. Generates a leaner `.env`. |
| `deployments/scripts/start.sh` | Removed REMOTE_WORKER_MODE branching. Seeds `deployments/.kube/config` from `k3d kubeconfig get … --internal`. |
| `deployments/scripts/utils.sh` | `patch_coredns_host_k3d_internal` now uses the docker bridge gateway IP — fixes pod → host port reachability for git-service / asdlc-api. |
| `deployments/scripts/stop.sh` | Removed remote-worker host-mode kill. |
| `deployments/single-cluster/values-thunder.yaml` | Added Thunder bootstrap scripts: `50-platform-users.sh`, **`58-openchoreo-workload-publisher.sh`**. Added `ou*` claims to console app's userAttributes. |
| `deployments/manifests/app-factory-coding-agent.yaml` | **New** — copied verbatim from `deployments-v2/wso2cloud-deployment/.../cluster-workflows/app-factory-coding-agent.yaml`. |
| `deployments/README.md` | **New** — quick-start + architecture diagram + file inventory. |
| `asdlc-service/config/config_loader.go` | Added `BFF_TASK_SIGNING_KEY_PATH` fallback for the Task JWT signing key (compose env-var substitution doesn't preserve newlines cleanly; mount the PEM instead). |
| `AGENTS.md` / `CLAUDE.md` | Updated the deployments/ line: from "DEPRECATED" to "ALTERNATIVE local setup". |

## Departures from the original v1

- **Database**: `database-service` + MySQL gone (deprecated).
- **Coding-agent**: no long-lived container; one-shot OC ClusterWorkflow pod.
- **collab-server**: removed (deferred per CLAUDE.md).
- **Login**: `admin` / `admin` (Thunder's built-in admin user, in `Administrators` group → bound to OC `admin` role).
- **OAuth apps**: in addition to v1's existing ones, we now bootstrap `openchoreo-workload-publisher-client` (OC v1 chart doesn't create it; v2 wso2cloud-deployment does — without it the build pipeline's `generate-workload-cr` step fails on the OAuth token request).
- **host.k3d.internal**: now points at the docker bridge gateway (where compose-published ports actually live), not the loadbalancer. v1's previous CoreDNS patch was correct for `*.openchoreo.localhost` routing via the loadbalancer but wrong for direct pod → host port access.

## Known minor issues

- The PR was opened directly with `draft: false` rather than via `gh pr create --draft` + `gh pr ready`. The BFF only listens for `pull_request.ready_for_review`, not `pull_request.opened (draft=false)`, so the task stays in `in_progress` until the user merges instead of advancing to `ready_for_review`. End-state (merged → built → deployed) is unaffected; only the intermediate label.
- v2's `observabilityplane` is not installed in v1 (kept it light). The console's "Live progress" panel shows `Live progress unavailable — falling back to status polling.` Status polling is sufficient for transitions.
