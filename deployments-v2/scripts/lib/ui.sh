#!/usr/bin/env bash
# UI helpers: consistent step logs, spinners, and time estimates.

set -u

_ui_red()    { printf '\033[31m%s\033[0m' "$1"; }
_ui_green()  { printf '\033[32m%s\033[0m' "$1"; }
_ui_yellow() { printf '\033[33m%s\033[0m' "$1"; }
_ui_dim()    { printf '\033[2m%s\033[0m' "$1"; }

log_section() { printf '\n============================================\n  %s\n============================================\n' "$1"; }
log_info()    { printf '   %s\n' "$1"; }
log_ok()      { printf '   '; _ui_green '✓'; printf ' %s\n' "$1"; }
log_skip()    { printf '   '; _ui_dim '— '; printf '%s\n' "$1"; }
log_warn()    { printf '   '; _ui_yellow '!'; printf ' %s\n' "$1"; }
log_fail()    { printf '   '; _ui_red '✗'; printf ' %s\n' "$1"; }

# log_step <message> [eta]
# Eta is shown dimmed in parentheses when provided.
log_step() {
  if [ $# -gt 1 ]; then
    printf '==> %s  ' "$1"
    _ui_dim "(may take ~$2)"
    printf '\n'
  else
    printf '==> %s\n' "$1"
  fi
}

die() { log_fail "$1"; exit 1; }

# ── spinner ───────────────────────────────────────────────────────────────────

_SPINNER_PID=""
_SPINNER_CHARS="⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

# Start a background spinner. Call spinner_stop to kill it.
# $1 = message   $2 = optional eta string (e.g. "3 min")
spinner_start() {
  local msg="$1" eta="${2:-}" prefix="" i=0
  [ -n "$eta" ] && prefix=" (may take ~${eta})"
  {
    while true; do
      printf '\r   %s %s%s' "${_SPINNER_CHARS:$i:1}" "$msg" "$prefix"
      i=$(( (i + 1) % ${#_SPINNER_CHARS} ))
      sleep 0.1
    done
  } &
  _SPINNER_PID=$!
}

# Kill the active spinner and clear its line.
spinner_stop() {
  [ -n "${_SPINNER_PID:-}" ] && kill "$_SPINNER_PID" 2>/dev/null || true
  _SPINNER_PID=""
  printf '\r\033[2K'
}

# spinner <msg> [eta] -- <command...>
# Runs a command silently with an animated spinner.  stdout+stderr are discarded.
# Returns the command's exit code and prints ✓ or ! accordingly.
spinner() {
  local msg="$1" eta=""
  shift
  if [ "${1:-}" != "--" ]; then
    eta="$1"; shift
  fi
  [ "${1:-}" = "--" ] && shift

  spinner_start "$msg" "$eta"
  "$@" &>/dev/null
  local rc=$?
  spinner_stop
  if [ $rc -eq 0 ]; then
    log_ok "done"
  else
    log_warn "failed (exit $rc)"
  fi
  return $rc
}
