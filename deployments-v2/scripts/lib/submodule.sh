#!/usr/bin/env bash
# lib/submodule.sh — ensure submodule cloned + on the right branch; auth-failure handling.
# Functions defined here; sourced by setup.sh.

set -u

SUBMODULE_PATH="$ROOT_DIR/deployments-v2/wso2cloud-deployment"
SUBMODULE_BRANCH="local-app-factory"

ensure_submodule() {
  if [ ! -d "$SUBMODULE_PATH/.git" ] && [ ! -f "$SUBMODULE_PATH/.git" ]; then
    log_info "submodule not yet checked out; running git submodule update"
    if ! git -C "$ROOT_DIR" submodule update --init --recursive deployments-v2/wso2cloud-deployment 2>&1; then
      log_fail "submodule clone failed (likely auth)"
      cat <<EOF
The submodule lives in a private repo (wso2-enterprise/wso2cloud-deployment).
Configure GitHub credentials before retrying:

  - macOS Keychain (recommended):
      git config --global credential.helper osxkeychain
      git ls-remote https://github.com/wso2-enterprise/wso2cloud-deployment.git
        # ^ enter PAT when prompted; helper persists it

  - Or via personal access token in URL:
      git config --global url."https://<USER>:<PAT>@github.com/".insteadOf "https://github.com/"
EOF
      exit 1
    fi
  fi

  local current
  current=$(git -C "$SUBMODULE_PATH" branch --show-current 2>/dev/null || echo "")
  if [ "$current" != "$SUBMODULE_BRANCH" ]; then
    log_warn "submodule on branch '$current'; expected '$SUBMODULE_BRANCH'"
    log_info "switching: git -C $SUBMODULE_PATH checkout $SUBMODULE_BRANCH"
    git -C "$SUBMODULE_PATH" checkout "$SUBMODULE_BRANCH"
  fi

  git -C "$SUBMODULE_PATH" fetch origin "$SUBMODULE_BRANCH" --quiet 2>/dev/null || true

  log_ok "submodule on $SUBMODULE_BRANCH"
}
