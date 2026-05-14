#!/usr/bin/env bash
# lib/env.sh — load .env, auto-generate secrets, prompt for missing values.
# Functions defined here; sourced by setup.sh / dev-cycle.sh / teardown.sh.

set -u

ENV_FILE="${ENV_FILE:-$ROOT_DIR/deployments-v2/.env}"
ENV_EXAMPLE="${ENV_EXAMPLE:-$ROOT_DIR/deployments-v2/.env.example}"
KEYS_DIR="${KEYS_DIR:-$ROOT_DIR/deployments-v2/keys}"

ensure_env_loaded() {
  [ -f "$ENV_FILE" ] || die "$ENV_FILE missing — run setup.sh first"
  set -a; source "$ENV_FILE"; set +a
  _export_task_signing_key
}

# Export PEM contents as BFF_TASK_SIGNING_KEY for envsubst into the BFF
# env-overlay. Newlines are escaped to literal \n; YAML double-quoted
# scalars decode them back to newlines, giving Go a multi-line PEM via
# os.Getenv. No-op if the PEM hasn't been generated yet.
_export_task_signing_key() {
  if [ -f "$KEYS_DIR/task-signing.pem" ]; then
    export BFF_TASK_SIGNING_KEY
    BFF_TASK_SIGNING_KEY=$(awk 'NR>1{printf "\\n"} {printf "%s", $0} END{printf "\\n"}' "$KEYS_DIR/task-signing.pem")
  fi
}

# ── helpers ──────────────────────────────────────────────────────────────────

# Read a value from the .env file (no sourcing, no side effects).
_env_read() {
  local var=$1 file=${2:-$ENV_FILE}
  local val; val=$(grep -m1 "^${var}=" "$file" 2>/dev/null | sed "s/^${var}=//")
  # If the value starts/ends with quotes, strip them (handle common .env quoting).
  val="${val#\"}"; val="${val%\"}"
  val="${val#\'}"; val="${val%\'}"
  echo "$val"
}

# Write a key=value line to the .env file, replacing an existing line if present.
_env_write() {
  local var=$1 val=$2 file=${3:-$ENV_FILE}
  if grep -q "^${var}=" "$file" 2>/dev/null; then
    sed -i.bak "s|^${var}=.*|${var}=${val}|" "$file"
    rm -f "$file.bak"
  else
    echo "${var}=${val}" >> "$file"
  fi
}

# Render the description for an env var from .env.example (comments in its section).
_env_desc() {
  local var=$1 example=$2
  awk -v var="$var" '
    BEGIN { lines = "" }
    /^# ----/ { lines = ""; next }
    /^#/ { lines = lines $0 "\n"; next }
    $0 ~ "^" var "=" {
      gsub(/\n# ?/, "\n", lines)
      sub(/^# ?/, "", lines)
      print lines
      exit
    }
    { lines = "" }
  ' "$example"
}

# Prompt the user for an env var, showing its description from .env.example.
# $1 = var name, $2 = current value (may be empty), $3 = is_required
# The parent scope's variable is updated via eval (since we can't use nameref cleanly).
_prompt_var() {
  local var=$1 current_val="${2:-}" is_required=${3:-0}
  local desc default_display label
  desc=$(_env_desc "$var" "$ENV_EXAMPLE")
  default_display="${current_val:-<skip>}"

  echo ""
  if [ -n "$desc" ]; then
    echo "$desc" | while IFS= read -r line; do
      log_info "  $line"
    done
  fi

  if [ "$is_required" = 1 ]; then
    label="required"
  else
    label="optional"
  fi

  printf "  [%s] %s [%s]: " "$label" "$var" "$default_display"
  IFS= read -r user_input

  if [ -n "$user_input" ]; then
    export "$var"="$user_input"
    _env_write "$var" "$user_input"
    return 0
  fi

  # User pressed Enter (accepted default / skipped).
  if [ -z "$current_val" ] && [ "$is_required" = 1 ]; then
    log_warn "skipped required var — AI features will be disabled until set"
    return 1
  fi

  return 0
}

_autogen_random_hex() {
  openssl rand -hex 32 2>/dev/null || {
    python3 -c "import secrets; print(secrets.token_hex(32))" 2>/dev/null || \
    die "cannot generate random hex (no openssl or python3)"
  }
}

_autogen_smee_url() {
  curl -sS -o /dev/null -w '%{redirect_url}' https://smee.io/new 2>/dev/null || {
    log_warn "could not provision smee.io channel — webhook forwarding disabled"
    return 1
  }
}

# ── main entry point ─────────────────────────────────────────────────────────

ensure_env() {
  # Bootstrap .env from example on first run.
  if [ ! -f "$ENV_FILE" ]; then
    log_info "creating .env from .env.example"
    cp "$ENV_EXAMPLE" "$ENV_FILE"
  fi

  # Legacy key migration: GITHUB_WEBHOOK_PROXY_URL → GITHUB_WEBHOOK_DELIVERY_URL.
  # Existing dev .env files have the old key; rewrite in place so the next
  # boot doesn't end up with an empty value under the new name.
  if grep -q "^GITHUB_WEBHOOK_PROXY_URL=" "$ENV_FILE" 2>/dev/null \
     && ! grep -q "^GITHUB_WEBHOOK_DELIVERY_URL=" "$ENV_FILE" 2>/dev/null; then
    sed -i.bak "s|^GITHUB_WEBHOOK_PROXY_URL=|GITHUB_WEBHOOK_DELIVERY_URL=|" "$ENV_FILE"
    rm -f "$ENV_FILE.bak"
    log_info "migrated legacy GITHUB_WEBHOOK_PROXY_URL → GITHUB_WEBHOOK_DELIVERY_URL in .env"
  fi

  # Read current values from .env (don't source — we build up exports manually).
  local _aev="" _gpv="" _grov="" _ptu="" _au="" _ap="" _gai="" _gci="" _gcs="" _gas="" _gkp=""
  _aev="$(_env_read ANTHROPIC_API_KEY)"
  _gpv="$(_env_read LOCAL_DEV_ADMIN_GITHUB_PAT)"
  _grov="$(_env_read LOCAL_DEV_ADMIN_GITHUB_OWNER)"
  _ptu="$(_env_read PUBLIC_THUNDER_URL)"
  _au="$(_env_read ADMIN_USERNAME)"
  _ap="$(_env_read ADMIN_PASSWORD)"

  # ── auto-generate secrets (no prompt) ──────────────────────────────────────

  if [ -z "$(_env_read GITHUB_WEBHOOK_SECRET)" ]; then
    local secret; secret=$(_autogen_random_hex)
    _env_write GITHUB_WEBHOOK_SECRET "$secret"
    log_info "auto-generated GITHUB_WEBHOOK_SECRET"
  fi
  if [ -z "$(_env_read OAUTH_STATE_SIGNING_KEY)" ]; then
    local oauth_key; oauth_key=$(_autogen_random_hex)
    _env_write OAUTH_STATE_SIGNING_KEY "$oauth_key"
    log_info "auto-generated OAUTH_STATE_SIGNING_KEY"
  fi
  if [ -z "$(_env_read GITHUB_WEBHOOK_DELIVERY_URL)" ]; then
    local smee; smee=$(_autogen_smee_url)
    if [ -n "$smee" ]; then
      _env_write GITHUB_WEBHOOK_DELIVERY_URL "$smee"
      log_info "auto-provisioned GITHUB_WEBHOOK_DELIVERY_URL (smee.io channel for local relay)"
    fi
  fi

  mkdir -p "$KEYS_DIR"
  if [ ! -f "$KEYS_DIR/task-signing.pem" ]; then
    log_info "generating task-signing RSA key"
    openssl genrsa -out "$KEYS_DIR/task-signing.pem" 2048 >/dev/null 2>&1 || \
      log_warn "could not generate task-signing.pem"
  fi

  # ── prompt for user-supplied values (only if empty in .env) ────────────────

  if [ "${DRY_RUN:-0}" != 1 ]; then
    log_section "Environment configuration"
    echo ""
    log_info "Values shown in [brackets] are the current default."
    log_info "Press Enter to skip optional values or accept the shown default."
    echo ""

    # ANTHROPIC_API_KEY
    if [ -z "$_aev" ]; then
      _prompt_var ANTHROPIC_API_KEY "" 1 || true
    fi

    # LOCAL_DEV_ADMIN_GITHUB_PAT — local-dev shortcut for pre-connecting
    # the admin user's GitHub PAT. Has no effect on hosted environments.
    if [ -z "$_gpv" ]; then
      _prompt_var LOCAL_DEV_ADMIN_GITHUB_PAT "" 0
    fi

    # LOCAL_DEV_ADMIN_GITHUB_OWNER (only prompt if PAT is set).
    _gpv="$(_env_read LOCAL_DEV_ADMIN_GITHUB_PAT)"
    _grov="$(_env_read LOCAL_DEV_ADMIN_GITHUB_OWNER)"
    if [ -n "$_gpv" ] && [ -z "$_grov" ]; then
      _prompt_var LOCAL_DEV_ADMIN_GITHUB_OWNER "" 1 || true
    fi
  fi

  # PUBLIC_THUNDER_URL — default if empty
  _ptu="$(_env_read PUBLIC_THUNDER_URL)"
  if [ -z "$_ptu" ]; then
    _env_write PUBLIC_THUNDER_URL "http://thunder.openchoreo.localhost:8080"
  fi

  # Admin creds — defaults if empty
  if [ -z "$_au" ]; then
    _env_write ADMIN_USERNAME "admin@openchoreo.dev"
  fi
  if [ -z "$_ap" ]; then
    _env_write ADMIN_PASSWORD "Admin@123"
  fi

  # ── source the finalized .env to export everything ─────────────────────────
  set -a; source "$ENV_FILE"; set +a
  _export_task_signing_key

  # Shell exports override .env (user might export vars on the CLI).
  local _shell_val
  _shell_val="${ANTHROPIC_API_KEY-__unset__}"
  [ "$_shell_val" != "__unset__" ] && export ANTHROPIC_API_KEY="$_shell_val"
  _shell_val="${LOCAL_DEV_ADMIN_GITHUB_PAT-__unset__}"
  [ "$_shell_val" != "__unset__" ] && export LOCAL_DEV_ADMIN_GITHUB_PAT="$_shell_val"
  _shell_val="${LOCAL_DEV_ADMIN_GITHUB_OWNER-__unset__}"
  [ "$_shell_val" != "__unset__" ] && export LOCAL_DEV_ADMIN_GITHUB_OWNER="$_shell_val"
  _shell_val="${LOCAL_DEV_ADMIN_OUHANDLE-__unset__}"
  [ "$_shell_val" != "__unset__" ] && export LOCAL_DEV_ADMIN_OUHANDLE="$_shell_val"

  # ── summary ────────────────────────────────────────────────────────────────
  if [ "${DRY_RUN:-0}" != 1 ]; then
    echo ""
    if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
      log_ok "ANTHROPIC_API_KEY: set"
    else
      log_warn "ANTHROPIC_API_KEY: not set — AI features disabled"
    fi
    if [ -n "${LOCAL_DEV_ADMIN_GITHUB_PAT:-}" ] && [ -n "${LOCAL_DEV_ADMIN_GITHUB_OWNER:-}" ]; then
      log_ok "LOCAL_DEV_ADMIN_GITHUB_PAT + LOCAL_DEV_ADMIN_GITHUB_OWNER: admin org '${LOCAL_DEV_ADMIN_OUHANDLE:-default}' will be pre-connected via the Connect API"
    else
      log_info "LOCAL_DEV_ADMIN_GITHUB_PAT: not set — connect via console (Settings → GitHub Integration)"
    fi
  fi
  log_ok "env ready"
}
