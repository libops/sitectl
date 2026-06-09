#!/bin/sh

request="$(cat)"
encoded="$(printf '%s' "$request" | base64 | tr -d '\n')"
printf '{"protocol_version":1,"ok":true,"output":"ARGS=%s\\nREQUEST_B64=%s\\nCOLUMNS=%s\\n"}\n' "$*" "$encoded" "$COLUMNS"
