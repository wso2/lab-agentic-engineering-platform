#!/usr/bin/env bash
# components.sh — sources of truth for asdlc local images.
#
# COMPONENTS: 4 long-lived services that get built into Workloads on the
# cluster. Workload name == component_name (matches source workload.yaml
# metadata.name).
#
# RUNNER_IMAGES: ephemeral runner images referenced by ClusterWorkflows
# (one pod per WorkflowRun, no Workload). Same fields as COMPONENTS but
# tagged statically (`:local`) and never `apply_workload`-ed.
#
# Format:
#   COMPONENTS:    <component_name>|<src_dir>|<dockerfile_path>|<build_context>|<hash_paths>
#   RUNNER_IMAGES: <component_name>|<src_dir>|<dockerfile_path>|<build_context>
#
# <hash_paths> (COMPONENTS only) is an optional space-separated list of
# repo-relative dirs whose content contributes to the image's content hash.
# If empty, defaults to <src_dir>. Use this when the Dockerfile COPYs from
# paths outside <src_dir> (e.g. console pulls in workspace deps from
# ui-components/). RUNNER_IMAGES are rebuilt unconditionally and don't
# participate in content-hash skipping.

# Order matters for dev-cycle.sh display only.
COMPONENTS=(
  "app-factory-console|console|console/Dockerfile|.|console ui-components"
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
#     IFS='|' read -r name src dockerfile context hash_paths <<<"$row"
#     [ -z "$hash_paths" ] && hash_paths="$src"
#     ...
#   done
