#!/usr/bin/env bash
# Smoke-tests a Dockerfile.scratch image: the API answers with the client key,
# and the control panel and management routes are absent.
# Usage: scratch-smoke-test.sh <image>
set -euo pipefail

image="$1"
key="smoke-test-key"
port=8317
work_dir="$(mktemp -d)"
container="scratch-smoke-$$"

cleanup() {
	docker rm -f "$container" >/dev/null 2>&1 || true
	rm -rf "$work_dir"
}
trap cleanup EXIT

cat >"$work_dir/config.yaml" <<EOF
server:
  port: $port
access:
  api-keys:
    - $key
management:
  disable-control-panel: true
oauth:
  auth-dir: /auths
EOF
chmod 0644 "$work_dir/config.yaml"

# Read-only root and a writable auth dir, as in the deployment.
docker run -d --name "$container" \
	--read-only \
	--tmpfs /auths:uid=65532,gid=65532,mode=0700 \
	-v "$work_dir/config.yaml:/config.yaml:ro" \
	-p "127.0.0.1:$port:$port" \
	"$image" --config /config.yaml >/dev/null

base="http://127.0.0.1:$port"
ready=false
for _ in $(seq 1 30); do
	if curl -s -o /dev/null "$base/v1/models"; then
		ready=true
		break
	fi
	sleep 1
done
if [ "$ready" != true ]; then
	echo "server did not start within 30 seconds"
	docker logs "$container"
	exit 1
fi

failed=0
check() {
	local want="$1" path="$2"
	shift 2
	local got
	got="$(curl -s -o /dev/null -w '%{http_code}' "$@" "$base$path")"
	if [ "$got" = "$want" ]; then
		echo "ok   $path -> $got"
	else
		echo "FAIL $path -> $got, want $want"
		failed=1
	fi
}

check 401 /v1/models
check 200 /v1/models -H "Authorization: Bearer $key"
check 404 /management.html
check 404 /v0/management/config
check 404 /v8/management/config

if [ "$failed" != 0 ]; then
	docker logs "$container"
	exit 1
fi
