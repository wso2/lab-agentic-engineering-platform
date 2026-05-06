#!/usr/bin/env bash
# components.sh — sources of truth for asdlc local images.
#
# COMPONENTS: 4 long-lived services that get built into Workloads on the
# cluster. Format: <component_name>|<src_dir>|<dockerfile_path>|<build_context>.
# Workload name == component_name (matches source workload.yaml metadata.name).
#
# RUNNER_IMAGES: ephemeral runner images referenced by ClusterWorkflows
# (one pod per WorkflowRun, no Workload). Same fields as COMPONENTS but
# tagged statically (`:local`) and never `apply_workload`-ed. Format same.

# Order matters for dev-cycle.sh display only.
COMPONENTS=(
  "app-factory-console|console|console/Dockerfile|."
  "app-factory-api|asdlc-service|asdlc-service/Dockerfile|asdlc-service"
  "app-factory-git-service|git-service|git-service/Dockerfile|git-service"
  "app-factory-agents-service|agents|agents/Dockerfile|agents"
)

# Image used by ClusterWorkflow `app-factory-coding-agent`. The runner pod
# is created per WorkflowRun by Argo — no Workload, no env-overlay; everything
# the runner needs flows in via {{workflow.parameters.*}} env vars.
RUNNER_IMAGES=(
  "app-factory-coding-agent-runner|remote-worker|remote-worker/Dockerfile|remote-worker"
)

# Iterate convenience:
#   for row in "${COMPONENTS[@]}"; do
#     IFS='|' read -r name src dockerfile context <<<"$row"
#     ...
#   done
