#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
set -eu

PROJECT_ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
# shellcheck source=../integration/baseline.env
. "$PROJECT_ROOT/integration/baseline.env"

mode=write
if [ "${1-}" = "--check" ]; then
	mode=check
	shift
fi
if [ "$#" -ne 0 ]; then
	echo "usage: scripts/generate-patch.sh [--check]" >&2
	exit 2
fi

upstream=${PICOCLAW_REPOSITORY:-$PICOCLAW_UPSTREAM_URL}
work_root=$(mktemp -d)
trap 'rm -rf "$work_root"' EXIT HUP INT TERM
checkout="$work_root/picoclaw"
candidate="$work_root/integration.patch"

git clone --quiet --no-checkout --filter=blob:none "$upstream" "$checkout"
git -C "$checkout" -c advice.detachedHead=false checkout --quiet --detach "$PICOCLAW_BASELINE"
actual=$(git -C "$checkout" rev-parse HEAD)
if [ "$actual" != "$PICOCLAW_BASELINE" ]; then
	echo "checked out $actual, expected $PICOCLAW_BASELINE" >&2
	exit 1
fi

cp -R "$PROJECT_ROOT/src/." "$checkout/"
GOTOOLCHAIN="$PICOCLAW_GO_TOOLCHAIN" go run "$PROJECT_ROOT/tools/edit-integration.go" "$checkout"

(
	cd "$checkout"
	GOTOOLCHAIN="$PICOCLAW_GO_TOOLCHAIN" go mod edit \
		-require="$MESHTASTIC_PROTOBUF_MODULE@$MESHTASTIC_PROTOBUF_VERSION" \
		-require="$SERIAL_MODULE@$SERIAL_VERSION" \
		-require="$PROTOBUF_MODULE@$PROTOBUF_VERSION"
	GOTOOLCHAIN="$PICOCLAW_GO_TOOLCHAIN" go mod tidy
	git diff --check
	git diff --no-ext-diff --binary --full-index -- \
		README.md \
		docs/guides/chat-apps.md \
		go.mod \
		go.sum \
		pkg/agent/agent_outbound.go \
		pkg/agent/agent_steering.go \
		pkg/channels/manager.go \
		pkg/config/config_channel.go \
		pkg/gateway/gateway.go
) >"$candidate"

patch="$PROJECT_ROOT/$INTEGRATION_PATCH"
if [ "$mode" = check ]; then
	if ! cmp -s "$candidate" "$patch"; then
		echo "$INTEGRATION_PATCH is stale; run scripts/generate-patch.sh" >&2
		diff -u "$patch" "$candidate" || true
		exit 1
	fi
	echo "$INTEGRATION_PATCH is reproducible and current"
	exit 0
fi

cp "$candidate" "$patch"
echo "generated $INTEGRATION_PATCH for $PICOCLAW_BASELINE"
