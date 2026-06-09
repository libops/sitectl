#!/bin/sh

printf '{"protocol_version":1,"ok":true,"output":"metadata=%s\\n"}\n' "$SITECTL_RPC_METADATA"
