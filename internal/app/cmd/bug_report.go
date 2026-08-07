// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"fmt"
	"runtime"

	"gitea.com/gitea/runner/internal/pkg/ver"

	"github.com/spf13/cobra"
)

// loadBugReportCmd prints environment details that are useful when opening a
// bug report, so users can paste them straight into an issue.
func loadBugReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bug-report",
		Short: "Print information useful when filing a bug report",
		Args:  cobra.MaximumNArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Runner version: %s\n", ver.Version())
			fmt.Fprintf(w, "Go version:     %s\n", runtime.Version())
			fmt.Fprintf(w, "OS/Arch:        %s/%s\n", runtime.GOOS, runtime.GOARCH)
			fmt.Fprintf(w, "NumCPU:         %d\n", runtime.NumCPU())
			return nil
		},
	}
}
