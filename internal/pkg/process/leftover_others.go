// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package process

import "context"

// KillProcessesWithCWDUnder is a no-op outside Windows: an open handle does not
// block os.RemoveAll there.
func KillProcessesWithCWDUnder(_ context.Context, _ []string) (int, error) {
	return 0, nil
}
