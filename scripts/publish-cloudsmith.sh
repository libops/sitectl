#!/usr/bin/env bash

set -euo pipefail
shopt -s nullglob

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-$ROOT_DIR/dist}"

: "${CLOUDSMITH_NAMESPACE:?CLOUDSMITH_NAMESPACE is required}"
: "${CLOUDSMITH_REPOSITORY:?CLOUDSMITH_REPOSITORY is required}"

publish_packages() {
  local format="$1"
  local pattern="$2"
  local targets="$3"
  local -a packages=()

  while IFS= read -r package; do
    packages+=("$package")
  done < <(find "$DIST_DIR" -maxdepth 1 -type f -name "$pattern" -print | sort)

  if [ ${#packages[@]} -eq 0 ]; then
    echo "No ${format} packages found in ${DIST_DIR}/"
    return 0
  fi
  if [ -z "${targets// }" ]; then
    echo "No Cloudsmith targets configured for ${format}; skipping"
    return 0
  fi

  for package in "${packages[@]}"; do
    for target in $targets; do
      cloudsmith push "$format" "$CLOUDSMITH_NAMESPACE/$CLOUDSMITH_REPOSITORY/$target" "$package" --no-wait-for-sync
    done
  done
}

publish_packages "deb" "*.deb" "${CLOUDSMITH_DEB_TARGETS:-}"
publish_packages "rpm" "*.rpm" "${CLOUDSMITH_RPM_TARGETS:-}"
publish_packages "alpine" "*.apk" "${CLOUDSMITH_ALPINE_TARGETS:-}"
