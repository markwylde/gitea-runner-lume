#!/bin/sh
set -eu

script=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/codesign-release-binary.sh
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
touch "$temporary/binary"

"$script" "$temporary/binary"

if RELEASE_REQUIRE_SIGNING=1 "$script" "$temporary/binary" >"$temporary/out" 2>"$temporary/err"; then
	echo "required signing unexpectedly succeeded without an identity" >&2
	exit 1
fi
grep -Fq "MACOS_SIGN_IDENTITY is required" "$temporary/err"
