#!/usr/bin/env bash
# components.sh — single source of truth for the 5 asdlc components we deploy.
# Format: <component_name>|<src_dir>|<dockerfile_path>|<build_context>|<hash_paths>
# - <hash_paths> is an optional space-separated list of repo-relative dirs whose
#   content contributes to the image's content hash. If empty, defaults to <src_dir>.
#   Use this when the Dockerfile COPYs from paths outside <src_dir> (e.g. console
#   pulls in workspace deps from ui-components/).
# Workload name == component_name (matches source workload.yaml metadata.name).

# Order matters for dev-cycle.sh display only.
COMPONENTS=(
  "app-factory-console|console|console/Dockerfile|.|console ui-components"
  "app-factory-api|asdlc-service|asdlc-service/Dockerfile|asdlc-service"
  "app-factory-git-service|git-service|git-service/Dockerfile|git-service"
  "app-factory-agents-service|agents|agents/Dockerfile|agents"
  "app-factory-remote-worker|remote-worker|remote-worker/Dockerfile|remote-worker"
)

# Iterate convenience:
#   for row in "${COMPONENTS[@]}"; do
#     IFS='|' read -r name src dockerfile context hash_paths <<<"$row"
#     [ -z "$hash_paths" ] && hash_paths="$src"
#     ...
#   done
