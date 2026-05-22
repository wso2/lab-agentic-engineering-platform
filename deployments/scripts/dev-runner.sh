#!/usr/bin/env bash
# dev-runner.sh — Build the coding-agent runner image locally and load it into
# k3d without pushing to Docker Hub. Use this during local skill/code iteration.
#
# Usage:
#   bash deployments/scripts/dev-runner.sh           # build + import
#   bash deployments/scripts/dev-runner.sh --revert  # restore production manifest
#
# After running, the ClusterWorkflow in the cluster is patched to use
# imagePullPolicy: Never + the local image tag. The on-disk manifest is
# untouched so the production values are preserved in git.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/env.sh"

REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
REMOTE_WORKER_DIR="${REPO_ROOT}/remote-worker"
LOCAL_IMAGE="app-factory-coding-agent-runner:local"
PROD_IMAGE="docker.io/xlight05/app-factory-coding-agent-runner:latest"
CLUSTER_WORKFLOW_NAME="app-factory-coding-agent"

revert() {
    echo "=== Reverting ClusterWorkflow to production image ==="
    kubectl --context "${CLUSTER_CONTEXT}" patch clusterworkflow "${CLUSTER_WORKFLOW_NAME}" \
        --type=json \
        -p='[
          {"op":"replace","path":"/spec/runTemplate/spec/templates/0/container/image","value":"'"${PROD_IMAGE}"'"},
          {"op":"replace","path":"/spec/runTemplate/spec/templates/0/container/imagePullPolicy","value":"Always"}
        ]'
    echo "Restored: image=${PROD_IMAGE}, imagePullPolicy=Always"
}

if [[ "${1:-}" == "--revert" ]]; then
    revert
    exit 0
fi

echo "=== Building runner image locally ==="
cd "${REMOTE_WORKER_DIR}"
docker build -t "${LOCAL_IMAGE}" .
echo "Built: ${LOCAL_IMAGE}"

echo "=== Importing image into k3d cluster '${CLUSTER_NAME}' ==="
k3d image import "${LOCAL_IMAGE}" -c "${CLUSTER_NAME}"
echo "Imported into k3d"

echo "=== Patching ClusterWorkflow to use local image ==="
kubectl --context "${CLUSTER_CONTEXT}" patch clusterworkflow "${CLUSTER_WORKFLOW_NAME}" \
    --type=json \
    -p='[
      {"op":"replace","path":"/spec/runTemplate/spec/templates/0/container/image","value":"'"${LOCAL_IMAGE}"'"},
      {"op":"replace","path":"/spec/runTemplate/spec/templates/0/container/imagePullPolicy","value":"Never"}
    ]'

echo ""
echo "Done. Next dispatch will use the local image."
echo "To restore production image: bash deployments/scripts/dev-runner.sh --revert"
