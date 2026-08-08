#!/usr/bin/env bash

set -euo pipefail

CORE_PATH="${1:-.}"

if [[ ! -f "${CORE_PATH}/go.mod" ]]; then
	echo "sitectl go.mod not found at ${CORE_PATH}" >&2
	exit 1
fi

GO_LINE="$(grep -E '^go [0-9]+([.][0-9]+)*$' "${CORE_PATH}/go.mod" || true)"
if [[ -z "${GO_LINE}" ]]; then
	echo "Unable to read Go directive from ${CORE_PATH}/go.mod" >&2
	exit 1
fi

SUPPORTED_PLUGIN_NAMES=(archivesspace drupal isle ojs omeka-classic omeka-s wp)
PLUGIN_DIRS=()
for plugin_name in "${SUPPORTED_PLUGIN_NAMES[@]}"; do
	plugin_dir="../sitectl-${plugin_name}"
	if [[ -f "${plugin_dir}/go.mod" ]]; then
		PLUGIN_DIRS+=("${plugin_dir}")
	fi
done

{
	echo "${GO_LINE}"
	echo
	echo "use ("
	echo "    ${CORE_PATH}"
	for plugin_dir in "${PLUGIN_DIRS[@]}"; do
		echo "    ${plugin_dir}"
	done
	echo ")"
} > go.work

echo "Wrote go.work for sitectl and ${#PLUGIN_DIRS[@]} local plugin checkout(s)"
