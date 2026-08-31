#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-only
set -eu

PROJECT_ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
# shellcheck source=../integration/baseline.env
. "$PROJECT_ROOT/integration/baseline.env"

if [ "$#" -ne 1 ]; then
	echo "usage: scripts/install.sh /path/to/picoclaw" >&2
	exit 2
fi

destination=$(CDPATH= cd -- "$1" 2>/dev/null && pwd) || {
	echo "destination does not exist or is not accessible: $1" >&2
	exit 1
}
if [ ! -d "$destination/.git" ]; then
	echo "destination is not a Git repository: $destination" >&2
	exit 1
fi
if [ ! -f "$destination/go.mod" ] ||
	! grep -qx 'module github.com/sipeed/picoclaw' "$destination/go.mod" ||
	[ ! -f "$destination/pkg/channels/interfaces.go" ] ||
	[ ! -f "$destination/pkg/gateway/gateway.go" ]; then
	echo "destination is not a recognized PicoClaw checkout: $destination" >&2
	exit 1
fi

actual=$(git -C "$destination" rev-parse HEAD 2>/dev/null) || {
	echo "cannot determine the destination commit" >&2
	exit 1
}
echo "PicoClaw commit: $actual"
if [ "$actual" != "$PICOCLAW_BASELINE" ]; then
	echo "unsupported PicoClaw baseline: $actual" >&2
	echo "supported baseline: $PICOCLAW_BASELINE" >&2
	exit 1
fi

patch="$PROJECT_ROOT/$INTEGRATION_PATCH"
if [ ! -f "$patch" ]; then
	echo "integration patch is missing: $patch" >&2
	exit 1
fi

copy_list=$(mktemp)
created_list=$(mktemp)
trap 'rm -f "$copy_list" "$created_list"' EXIT HUP INT TERM
(
	cd "$PROJECT_ROOT/src"
	find . -type f -print | LC_ALL=C sort
) >"$copy_list"
: >"$created_list"

all_present=true
while IFS= read -r rel; do
	rel=${rel#./}
	source_file="$PROJECT_ROOT/src/$rel"
	target_file="$destination/$rel"
	if [ -e "$target_file" ]; then
		if [ ! -f "$target_file" ] || ! cmp -s "$source_file" "$target_file"; then
			echo "refusing to overwrite existing non-identical path: $rel" >&2
			exit 1
		fi
	else
		all_present=false
	fi
done <"$copy_list"

patch_mode=
if git -C "$destination" apply --check --whitespace=error-all "$patch" 2>/dev/null; then
	patch_mode=apply
elif git -C "$destination" apply --reverse --check --whitespace=error-all "$patch" 2>/dev/null; then
	patch_mode=present
else
	echo "integration patch does not apply cleanly to the current working tree" >&2
	echo "no files were modified; inspect local changes or use the supported baseline" >&2
	exit 1
fi

if [ "$patch_mode" = present ] && [ "$all_present" = true ]; then
	echo "Meshtastic integration is already installed and matches this project"
	exit 0
fi

while IFS= read -r rel; do
	rel=${rel#./}
	source_file="$PROJECT_ROOT/src/$rel"
	target_file="$destination/$rel"
	if [ ! -e "$target_file" ]; then
		mkdir -p "$(dirname "$target_file")"
		cp "$source_file" "$target_file"
		printf '%s\n' "$rel" >>"$created_list"
	fi
done <"$copy_list"

if [ "$patch_mode" = apply ]; then
	if ! git -C "$destination" apply --whitespace=error-all "$patch"; then
		while IFS= read -r rel; do
			rm -f "$destination/$rel"
		done <"$created_list"
		echo "patch application failed after preflight; newly copied files were removed" >&2
		exit 1
	fi
fi

echo "installed native Meshtastic support into $destination"
echo "review with: git -C '$destination' status --short"
