#!/usr/bin/env bash
# lib/images.sh — content_hash, build_image, import_image.
# Functions defined here; sourced by setup.sh / dev-cycle.sh.

set -u

content_hash() {
  # Hash one or more dirs together. Tracked + uncommitted files contribute.
  local d
  ( for d in "$@"; do
      ( cd "$d" && { git ls-files; git ls-files -mo --exclude-standard; } \
        | sort -u | xargs sha256sum 2>/dev/null )
    done | sort -u | sha256sum | cut -c1-12 )
}

build_image() {
  local name=$1 src_dir=$2 dockerfile=$3 context=$4 image=$5
  # If context is already absolute, use it as-is; otherwise resolve from ROOT_DIR.
  if [ "${context:0:1}" = "/" ]; then
    local ctx="$context"
  elif [ "$context" = "." ]; then
    local ctx="$ROOT_DIR"
  else
    local ctx="$ROOT_DIR/$context"
  fi
  log_info "building $name (may take ~2 min)"
  docker build -t "$image" -f "$ROOT_DIR/$dockerfile" "$ctx" \
    || die "docker build failed for $name"
  log_ok "$name built"
}

import_image() {
  local image=$1
  log_info "importing $image into k3d"
  k3d image import "$image" -c "$K3D_CLUSTER_NAME" \
    || die "k3d image import failed for $image"
  log_ok "image imported"
}
