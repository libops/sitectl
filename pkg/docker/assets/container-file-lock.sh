#!/bin/sh

# Invocation contract:
#   sh container-file-lock.sh ABSOLUTE_LOCK_PATH
#
# The outer process acquires the advisory lock and propagates flock's exit
# status. The inner holder removes this staged program, announces acquisition,
# and holds the lock while heartbeats arrive on stdin. EOF or 30 seconds
# without input releases an abandoned lock.

if [ "${1-}" = "--hold" ]; then
  program_path=${2-}
  rm -f -- "$program_path" || true

  ( IFS= read -r -t 0 _ </dev/null )
  status=$?
  if [ "$status" -gt 1 ]; then
    printf 'container shell does not support read timeouts\n' >&2
    exit 64
  fi

  printf 'sitectl-container-file-lock-acquired\n'
  while IFS= read -r -t 30 message; do
    [ "$message" = release ] && exit 0
  done
  exit 0
fi

lock_path=${1-}
if [ -z "$lock_path" ]; then
  printf 'an absolute lock path is required\n' >&2
  rm -f -- "$0" || true
  exit 64
fi

flock -n "$lock_path" sh "$0" --hold "$0"
status=$?
rm -f -- "$0" || true
exit "$status"
