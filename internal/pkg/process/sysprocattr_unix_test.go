// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !windows && !plan9

package process

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSysProcAttrUnixModes(t *testing.T) {
	plain := SysProcAttr("", false)
	require.True(t, plain.Setpgid)
	require.False(t, plain.Setsid)

	tty := SysProcAttr("", true)
	require.True(t, tty.Setsid)
	require.True(t, tty.Setctty)
	require.False(t, tty.Setpgid)
}
