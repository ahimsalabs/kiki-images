#!/bin/bash
# build.sh [docker buildx build flags...]
#
# Builds images/agent with every line of versions.env as a --build-arg,
# SOURCE_DATE_EPOCH among them. Pass the output with rewrite-timestamp=true,
# so that layers built from the same inputs have the same digests; that
# needs a docker-container builder (docker buildx create --use). Locally:
#   images/agent/build.sh --output type=docker,name=agent,rewrite-timestamp=true
set -euo pipefail
dir=$(cd "$(dirname "$0")" && pwd)
args=()
while IFS= read -r line; do
	[[ $line =~ ^[A-Za-z_][A-Za-z0-9_]*= ]] && args+=(--build-arg "$line")
done <"$dir/versions.env"
# Inside a kiki VM with egress interception, hand the build its CA bundle.
if [[ -s /run/kiki/ca-bundle.crt ]]; then
	args+=(--secret id=ca,src=/run/kiki/ca-bundle.crt)
fi
exec docker buildx build "${args[@]}" "$@" "$dir"
