#!/bin/sh

stdin="$(cat)"
printf '{"protocol_version":1,"ok":true,"output":"ARGS=%s\\nSTDIN=%s\\n"}\n' "$*" "$stdin"
