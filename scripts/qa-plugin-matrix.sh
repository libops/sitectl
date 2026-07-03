#!/usr/bin/env bash
set -euo pipefail

apps_default=(archivesspace drupal isle ojs omeka-classic omeka-s wp)

qa_target="${SITECTL_QA_TARGET:-remote}"
qa_apps="${SITECTL_QA_APPS:-${apps_default[*]}}"
qa_phases="${SITECTL_QA_PHASES:-default-http domain-http mkcert letsencrypt cloudflare}"
qa_remote_host="${SITECTL_QA_REMOTE_HOST:-}"
qa_remote_user="${SITECTL_QA_REMOTE_USER:-root}"
qa_remote_port="${SITECTL_QA_REMOTE_PORT:-22}"
qa_remote_key="${SITECTL_QA_SSH_KEY:-}"
qa_remote_base="${SITECTL_QA_REMOTE_BASE:-/root/sitectl-plugin-qa}"
qa_local_base="${SITECTL_QA_LOCAL_BASE:-${TMPDIR:-/tmp}/sitectl-plugin-qa}"
qa_domain="${SITECTL_QA_DOMAIN:-qa-origin.libops.io}"
qa_acme_email="${SITECTL_QA_ACME_EMAIL:-}"
qa_origin_host="${SITECTL_QA_ORIGIN_HOST:-${qa_remote_host:-127.0.0.1}}"
qa_keep="${SITECTL_QA_KEEP:-false}"
qa_curl="${SITECTL_QA_CURL:-true}"
qa_healthcheck_timeout="${SITECTL_QA_HEALTHCHECK_TIMEOUT:-15m}"
qa_healthcheck_interval="${SITECTL_QA_HEALTHCHECK_INTERVAL:-15s}"
qa_cloudflare_domain="${SITECTL_QA_CLOUDFLARE_DOMAIN:-${qa_domain}}"
qa_cloudflare_cert="${SITECTL_QA_CLOUDFLARE_CERT:-}"
qa_cloudflare_key="${SITECTL_QA_CLOUDFLARE_KEY:-}"
qa_custom_domain="${SITECTL_QA_CUSTOM_DOMAIN:-${qa_domain}}"
qa_custom_cert="${SITECTL_QA_CUSTOM_CERT:-}"
qa_custom_key="${SITECTL_QA_CUSTOM_KEY:-}"
qa_allow_skips="${SITECTL_QA_ALLOW_SKIPS:-false}"

usage() {
	cat <<'USAGE'
Run sitectl plugin QA against local or remote Docker hosts.

Environment:
  SITECTL_QA_TARGET=remote|local
  SITECTL_QA_REMOTE_HOST=172.239.194.15
  SITECTL_QA_REMOTE_USER=root
  SITECTL_QA_SSH_KEY=$HOME/.ssh/id_rsa
  SITECTL_QA_DOMAIN=qa-origin.libops.io
  SITECTL_QA_ACME_EMAIL=admin@example.org
  SITECTL_QA_PHASES="default-http domain-http mkcert letsencrypt cloudflare custom"
  SITECTL_QA_APPS="wp drupal isle"
  SITECTL_QA_KEEP=true
  SITECTL_QA_ALLOW_SKIPS=true

Cloudflare/custom TLS phases require:
  SITECTL_QA_CLOUDFLARE_CERT=/path/to/cert.pem
  SITECTL_QA_CLOUDFLARE_KEY=/path/to/privkey.pem
  SITECTL_QA_CUSTOM_CERT=/path/to/cert.pem
  SITECTL_QA_CUSTOM_KEY=/path/to/privkey.pem
USAGE
}

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 1
	fi
}

log() {
	printf '\n[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" >&2
}

skip_phase() {
	local phase="$1"
	local reason="$2"
	if [ "$qa_allow_skips" = "true" ]; then
		log "skipping ${phase}: ${reason}"
		return 0
	fi
	echo "cannot run ${phase}: ${reason}; set SITECTL_QA_ALLOW_SKIPS=true to allow partial QA" >&2
	return 1
}

run() {
	log "$*"
	"$@"
}

remote_ssh() {
	local args=(-p "$qa_remote_port")
	if [ -n "$qa_remote_key" ]; then
		args+=(-i "$qa_remote_key")
	fi
	ssh "${args[@]}" "${qa_remote_user}@${qa_remote_host}" "$@"
}

remote_scp_to() {
	local source="$1"
	local target="$2"
	local args=(-P "$qa_remote_port")
	if [ -n "$qa_remote_key" ]; then
		args+=(-i "$qa_remote_key")
	fi
	scp "${args[@]}" "$source" "${qa_remote_user}@${qa_remote_host}:${target}"
}

context_name() {
	local app="$1"
	printf 'qa-%s-%s' "$qa_target" "$app"
}

project_dir() {
	local app="$1"
	case "$qa_target" in
		remote) printf '%s/%s' "$qa_remote_base" "$app" ;;
		local) printf '%s/%s' "$qa_local_base" "$app" ;;
		*) echo "unsupported SITECTL_QA_TARGET: $qa_target" >&2; exit 1 ;;
	esac
}

create_extra_args() {
	local app="$1"
	case "$app" in
		isle)
			printf '%s\n' \
				--fcrepo off \
				--blazegraph off \
				--isle-file-system-uri private \
				--iiif triplet \
				--iiif-topology disabled \
				--bot-mitigation off
			;;
	esac
}

create_stack() {
	local app="$1"
	local ctx="$2"
	local dir="$3"
	local args=(
		create "${app}/default"
		--type "$qa_target"
		--checkout-source template
		--context "$ctx"
		--project-dir "$dir"
		--site "$ctx"
		--environment qa
		--project-name "$ctx"
		--compose-project-name "$app"
		--yolo
	)
	if [ "$qa_target" = "remote" ]; then
		args+=(--ssh-hostname "$qa_remote_host" --ssh-user "$qa_remote_user" --ssh-port "$qa_remote_port")
		if [ -n "$qa_remote_key" ]; then
			args+=(--ssh-key "$qa_remote_key")
		fi
	fi
	while IFS= read -r arg; do
		[ -n "$arg" ] && args+=("$arg")
	done < <(create_extra_args "$app")
	run sitectl "${args[@]}"
}

healthcheck_stack() {
	local ctx="$1"
	run sitectl healthcheck --context "$ctx" --persist --timeout "$qa_healthcheck_timeout" --interval "$qa_healthcheck_interval"
}

set_ingress() {
	local ctx="$1"
	shift
	run sitectl component set ingress --context "$ctx" --yolo "$@"
	run sitectl compose --context "$ctx" up
	healthcheck_stack "$ctx"
}

curl_url() {
	local url="$1"
	local host="$2"
	local port="$3"
	local tls_mode="${4:-strict}"
	[ "$qa_curl" = "true" ] || return 0
	local args=(-fsSIL --max-time 45 --resolve "${host}:${port}:${qa_origin_host}" "$url")
	if [ "$tls_mode" = "insecure" ]; then
		args=(-k "${args[@]}")
	fi
	run curl "${args[@]}" >/dev/null
}

install_cert_pair() {
	local dir="$1"
	local cert="$2"
	local key="$3"
	if [ -z "$cert" ] || [ -z "$key" ]; then
		echo "certificate and key paths are required for this TLS phase" >&2
		return 1
	fi
	if [ "$qa_target" = "remote" ]; then
		run remote_ssh "mkdir -p '${dir}/certs'"
		run remote_scp_to "$cert" "${dir}/certs/cert.pem"
		run remote_scp_to "$key" "${dir}/certs/privkey.pem"
	else
		run mkdir -p "$dir/certs"
		run cp "$cert" "$dir/certs/cert.pem"
		run cp "$key" "$dir/certs/privkey.pem"
	fi
}

phase_default_http() {
	local ctx="$1"
	healthcheck_stack "$ctx"
	if [ "$qa_target" = "remote" ]; then
		run curl -fsSIL --max-time 45 "http://${qa_origin_host}/" >/dev/null
	fi
}

phase_domain_http() {
	local ctx="$1"
	set_ingress "$ctx" --mode http --domain "$qa_domain"
	curl_url "http://${qa_domain}/" "$qa_domain" 80 strict
}

phase_mkcert() {
	local ctx="$1"
	set_ingress "$ctx" --mode https-mkcert --domain "$qa_domain"
	curl_url "https://${qa_domain}/" "$qa_domain" 443 insecure
}

phase_letsencrypt() {
	local ctx="$1"
	if [ -z "$qa_acme_email" ]; then
		skip_phase "letsencrypt" "SITECTL_QA_ACME_EMAIL is not set"
		return $?
	fi
	set_ingress "$ctx" --mode https-letsencrypt --domain "$qa_domain" --acme-email "$qa_acme_email"
	curl_url "https://${qa_domain}/" "$qa_domain" 443 strict
}

phase_cloudflare() {
	local ctx="$1"
	local dir="$2"
	if [ -z "$qa_cloudflare_cert" ] || [ -z "$qa_cloudflare_key" ]; then
		skip_phase "cloudflare" "SITECTL_QA_CLOUDFLARE_CERT and SITECTL_QA_CLOUDFLARE_KEY are not set"
		return $?
	fi
	install_cert_pair "$dir" "$qa_cloudflare_cert" "$qa_cloudflare_key"
	set_ingress "$ctx" --mode https-cloudflare-origin --domain "$qa_cloudflare_domain"
	curl_url "https://${qa_cloudflare_domain}/" "$qa_cloudflare_domain" 443 insecure
}

phase_custom() {
	local ctx="$1"
	local dir="$2"
	if [ -z "$qa_custom_cert" ] || [ -z "$qa_custom_key" ]; then
		skip_phase "custom" "SITECTL_QA_CUSTOM_CERT and SITECTL_QA_CUSTOM_KEY are not set"
		return $?
	fi
	install_cert_pair "$dir" "$qa_custom_cert" "$qa_custom_key"
	set_ingress "$ctx" --mode https-custom --domain "$qa_custom_domain"
	curl_url "https://${qa_custom_domain}/" "$qa_custom_domain" 443 insecure
}

teardown_stack() {
	local ctx="$1"
	[ "$qa_keep" = "true" ] && return 0
	run sitectl compose --context "$ctx" down -v || true
}

main() {
	if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
		usage
		return 0
	fi
	need sitectl
	need curl
	if [ "$qa_target" = "remote" ] && [ -z "$qa_remote_host" ]; then
		echo "SITECTL_QA_REMOTE_HOST is required when SITECTL_QA_TARGET=remote" >&2
		exit 1
	fi
	if [ "$qa_target" = "remote" ]; then
		need ssh
		need scp
	else
		run mkdir -p "$qa_local_base"
	fi

	for app in $qa_apps; do
		local ctx dir
		ctx="$(context_name "$app")"
		dir="$(project_dir "$app")"
		log "starting ${app} (${ctx})"
		create_stack "$app" "$ctx" "$dir"
		for phase in $qa_phases; do
			case "$phase" in
				default-http) phase_default_http "$ctx" ;;
				domain-http) phase_domain_http "$ctx" ;;
				mkcert|self-signed) phase_mkcert "$ctx" ;;
				letsencrypt) phase_letsencrypt "$ctx" ;;
				cloudflare) phase_cloudflare "$ctx" "$dir" ;;
				custom) phase_custom "$ctx" "$dir" ;;
				*) echo "unknown QA phase: $phase" >&2; exit 1 ;;
			esac
		done
		teardown_stack "$ctx"
	done
}

main "$@"
