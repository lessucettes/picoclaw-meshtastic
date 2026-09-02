#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
set -eu

PROJECT_ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
# shellcheck source=../integration/baseline.env
. "$PROJECT_ROOT/integration/baseline.env"

"$PROJECT_ROOT/scripts/generate-patch.sh" --check

unformatted=$(gofmt -l "$PROJECT_ROOT/src" "$PROJECT_ROOT/tools")
if [ -n "$unformatted" ]; then
	echo "unformatted Go files:" >&2
	echo "$unformatted" >&2
	exit 1
fi

upstream=${PICOCLAW_REPOSITORY:-$PICOCLAW_UPSTREAM_URL}
work_root=$(mktemp -d)
trap 'rm -rf "$work_root"' EXIT HUP INT TERM
checkout="$work_root/picoclaw"

git clone --quiet --no-checkout --filter=blob:none "$upstream" "$checkout"
git -C "$checkout" -c advice.detachedHead=false checkout --quiet --detach "$PICOCLAW_BASELINE"
"$PROJECT_ROOT/scripts/install.sh" "$checkout"
"$PROJECT_ROOT/scripts/install.sh" "$checkout"

git -C "$checkout" diff --check
git -C "$checkout" apply --reverse --check "$PROJECT_ROOT/$INTEGRATION_PATCH"
(
	cd "$checkout"
	CGO_ENABLED=0 GOTOOLCHAIN="$PICOCLAW_GO_TOOLCHAIN" go build -tags=goolm,stdjson -o "$work_root/picoclaw" ./cmd/picoclaw
	CGO_ENABLED=0 GOTOOLCHAIN="$PICOCLAW_GO_TOOLCHAIN" go test -tags=goolm,stdjson ./pkg/channels/meshtastic ./pkg/bus ./pkg/channels ./pkg/config ./pkg/gateway
	# This pinned upstream test has an unsynchronized buffered-channel handoff and
	# can fail under load. Keep its coverage, but isolate it from the full suite.
	late_steering_test='^TestAgentLoop_Run_AutoContinuesLateSteeringMessage$'
	CGO_ENABLED=0 GOTOOLCHAIN="$PICOCLAW_GO_TOOLCHAIN" go test -tags=goolm,stdjson ./pkg/agent -skip "$late_steering_test"
	CGO_ENABLED=0 GOTOOLCHAIN="$PICOCLAW_GO_TOOLCHAIN" go test -tags=goolm,stdjson ./pkg/agent -run "$late_steering_test" -count=10
	CGO_ENABLED=1 GOTOOLCHAIN="$PICOCLAW_GO_TOOLCHAIN" go test -tags=goolm,stdjson -race ./pkg/channels/meshtastic
	CGO_ENABLED=1 GOTOOLCHAIN="$PICOCLAW_GO_TOOLCHAIN" go test -tags=goolm,stdjson -race ./pkg/agent -run '^TestPublishResponseForInboundIfNeeded_PreservesMessageID$'
)

echo "clean-room verification passed for $PICOCLAW_BASELINE"
