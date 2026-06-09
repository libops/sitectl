#!/bin/sh

if [ "$1" = "__sitectl-rpc" ]; then
  if [ -n "$SITECTL_TEST_PLUGIN_STDERR" ]; then
    printf '%s\n' "$SITECTL_TEST_PLUGIN_STDERR" >&2
  fi
  printf '%s\n' "$SITECTL_TEST_RPC_RESPONSE"
  exit 0
fi

case "$SITECTL_TEST_PLUGIN_FALLBACK" in
  create-help)
    if [ "$1" = "create" ] && [ "$2" = "--help" ]; then
      exit 0
    fi
    ;;
  "")
    ;;
  *)
    printf '%s\n' "unknown fixture fallback: $SITECTL_TEST_PLUGIN_FALLBACK" >&2
    exit 64
    ;;
esac

exit 1
