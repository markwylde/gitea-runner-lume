// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

//go:build !darwin

package guestagent

import "context"

func waitForConsoleGUISession(context.Context) error { return nil }
