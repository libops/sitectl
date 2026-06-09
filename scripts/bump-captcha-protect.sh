#!/usr/bin/env bash
set -euo pipefail

repo="${CAPTCHA_PROTECT_REPO:-libops/captcha-protect}"
go_file="${CAPTCHA_PROTECT_GO_FILE:-pkg/services/traefik/bot_mitigation.go}"
tag="${CAPTCHA_PROTECT_TAG:-${1:-}}"

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 1
	fi
}

need curl
need find
need mktemp
need sed
need sort
need unzip

if command -v sha256sum >/dev/null 2>&1; then
	sha256_file() { sha256sum "$1" | sed 's/[[:space:]].*//'; }
	sha256_stdin() { sha256sum | sed 's/[[:space:]].*//'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256_file() { shasum -a 256 "$1" | sed 's/[[:space:]].*//'; }
	sha256_stdin() { shasum -a 256 | sed 's/[[:space:]].*//'; }
else
	echo "missing required command: sha256sum or shasum" >&2
	exit 1
fi

curl_headers=(
	-H "Accept: application/vnd.github+json"
	-H "X-GitHub-Api-Version: 2022-11-28"
	-H "User-Agent: sitectl-captcha-protect-bump"
)
if [ -n "${GITHUB_TOKEN:-}" ]; then
	curl_headers+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
fi

if [ -z "$tag" ]; then
	tag="$(
		curl -fsSL "${curl_headers[@]}" "https://api.github.com/repos/${repo}/releases/latest" \
			| sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
			| head -n 1
	)"
fi
if [ -z "$tag" ]; then
	echo "could not resolve latest captcha-protect release tag" >&2
	exit 1
fi

source_url="https://github.com/${repo}/archive/refs/tags/${tag}.zip"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

archive="${tmp_dir}/captcha-protect.zip"
extract_dir="${tmp_dir}/extract"
mkdir -p "$extract_dir"

curl -fsSL -A "sitectl-captcha-protect-bump" -o "$archive" "$source_url"
archive_sha256="$(sha256_file "$archive")"
unzip -q "$archive" -d "$extract_dir"

root_count="$(find "$extract_dir" -mindepth 1 -maxdepth 1 -type d | wc -l | sed 's/[[:space:]]//g')"
if [ "$root_count" != "1" ]; then
	echo "expected one top-level directory in captcha-protect archive, found ${root_count}" >&2
	exit 1
fi
root_dir="$(find "$extract_dir" -mindepth 1 -maxdepth 1 -type d | head -n 1)"

rm -rf "${root_dir}/ci" "${root_dir}/.github" "${root_dir}/renovate.json5"
find "$root_dir" -type f -name '*_test.go' -exec rm -f {} +

non_regular="$(find "$root_dir" ! -type d ! -type f -print -quit)"
if [ -n "$non_regular" ]; then
	echo "captcha-protect source contains non-regular file ${non_regular#"${root_dir}"/}" >&2
	exit 1
fi

tree_sha256="$(
	cd "$root_dir"
	{
		find . -type f -print0 \
			| LC_ALL=C sort -z \
			| while IFS= read -r -d '' path; do
				rel="${path#./}"
				printf 'file\000%s\000' "$rel"
				cat -- "$rel"
				printf '\000'
			done
	} | sha256_stdin
)"

updated="${tmp_dir}/bot_mitigation.go"
sed \
	-e 's|\([[:space:]]*captchaProtectSourceURL[[:space:]]*=[[:space:]]*\)"[^"]*"|\1"'"$source_url"'"|' \
	-e 's|\([[:space:]]*captchaProtectSourceSHA256[[:space:]]*=[[:space:]]*\)"[^"]*"|\1"'"$archive_sha256"'"|' \
	-e 's|\([[:space:]]*captchaProtectExtractedTreeSHA256[[:space:]]*=[[:space:]]*\)"[^"]*"|\1"'"$tree_sha256"'"|' \
	"$go_file" > "$updated"

if ! grep -q "captchaProtectSourceURL[[:space:]]*=[[:space:]]*\"${source_url}\"" "$updated"; then
	echo "failed to update captchaProtectSourceURL in ${go_file}" >&2
	exit 1
fi
if ! grep -q "captchaProtectSourceSHA256[[:space:]]*=[[:space:]]*\"${archive_sha256}\"" "$updated"; then
	echo "failed to update captchaProtectSourceSHA256 in ${go_file}" >&2
	exit 1
fi
if ! grep -q "captchaProtectExtractedTreeSHA256[[:space:]]*=[[:space:]]*\"${tree_sha256}\"" "$updated"; then
	echo "failed to update captchaProtectExtractedTreeSHA256 in ${go_file}" >&2
	exit 1
fi

mv "$updated" "$go_file"

echo "captcha-protect ${tag}"
echo "source_url=${source_url}"
echo "archive_sha256=${archive_sha256}"
echo "tree_sha256=${tree_sha256}"
