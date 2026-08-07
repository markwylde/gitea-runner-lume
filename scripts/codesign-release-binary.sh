#!/bin/sh
set -eu

binary=${1:?release binary path is required}

if [ -z "${MACOS_SIGN_IDENTITY:-}" ]; then
	if [ "${RELEASE_REQUIRE_SIGNING:-0}" = "1" ]; then
		echo "MACOS_SIGN_IDENTITY is required for a production release" >&2
		exit 1
	fi
	exit 0
fi

codesign --force --options runtime --timestamp --sign "$MACOS_SIGN_IDENTITY" "$binary"
codesign --verify --strict --verbose=2 "$binary"
