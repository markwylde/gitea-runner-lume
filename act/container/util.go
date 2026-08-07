// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build (!windows && !plan9 && !openbsd) || (!windows && !plan9 && !mips64)

package container

import (
	"os"

	"github.com/creack/pty"
)

func openPty() (*os.File, *os.File, error) {
	return pty.Open()
}
