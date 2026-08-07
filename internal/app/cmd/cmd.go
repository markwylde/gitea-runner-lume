// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"fmt"
	"os"

	"gitea.com/gitea/runner/internal/pkg/ver"

	"github.com/spf13/cobra"
)

func Execute(ctx context.Context) {
	rootCmd := NewRootCommand(ctx)
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// NewRootCommand constructs the complete CLI without executing it.
func NewRootCommand(ctx context.Context) *cobra.Command {
	// ./gitea-runner-lume
	rootCmd := &cobra.Command{
		Use:          "gitea-runner-lume",
		Short:        "Gitea Runner with disposable Lume macOS workers",
		Version:      ver.Version(),
		SilenceUsage: true,
	}
	rootCmd.SetVersionTemplate("gitea-runner-lume {{.Version}}\n")
	configFile := ""
	rootCmd.PersistentFlags().StringVarP(&configFile, "config", "c", "", "Config file path. `config` subcommands fall back to config.yaml in the working directory or next to the executable")

	// ./gitea-runner register
	var regArgs registerArgs
	registerCmd := &cobra.Command{
		Use:   "register",
		Short: "Register a runner to the server",
		Args:  cobra.MaximumNArgs(0),
		RunE:  runRegister(ctx, &regArgs, &configFile), // must use a pointer to regArgs
	}
	registerCmd.Flags().BoolVar(&regArgs.NoInteractive, "no-interactive", false, "Disable interactive mode")
	registerCmd.Flags().StringVar(&regArgs.InstanceAddr, "instance", "", "Gitea instance address")
	registerCmd.Flags().StringVar(&regArgs.Token, "token", "", "Runner token (or set the GITEA_RUNNER_REGISTRATION_TOKEN envvar)")
	registerCmd.Flags().StringVar(&regArgs.TokenFile, "token-file", "", "Path to a file containing the runner token")
	registerCmd.Flags().StringVar(&regArgs.RunnerName, "name", "", "Runner name")
	registerCmd.Flags().StringVar(&regArgs.Labels, "labels", "", "Runner tags, comma separated")
	registerCmd.Flags().BoolVar(&regArgs.Ephemeral, "ephemeral", false, "Configure the runner to be ephemeral and only ever be able to pick a single job (stricter than --once)")
	rootCmd.AddCommand(registerCmd)

	// ./gitea-runner daemon
	var daemArgs daemonArgs
	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run as a runner daemon",
		Args:  cobra.MaximumNArgs(0),
		RunE:  runDaemon(ctx, &daemArgs, &configFile),
	}
	daemonCmd.Flags().BoolVar(&daemArgs.Once, "once", false, "Run one job then exit")
	daemonCmd.Flags().StringVar(&daemArgs.Labels, "labels", os.Getenv("GITEA_RUNNER_LABELS"), "Runner labels, comma separated. Overrides the labels of an already registered runner")
	rootCmd.AddCommand(daemonCmd)

	// ./gitea-runner exec
	rootCmd.AddCommand(loadExecCmd(ctx))

	// ./gitea-runner bug-report
	rootCmd.AddCommand(loadBugReportCmd())

	// ./gitea-runner config
	rootCmd.AddCommand(loadConfigCmd(&configFile))

	// ./gitea-runner generate-config
	generateConfigCmd := loadGenerateConfigCmd("generate-config")
	generateConfigCmd.Deprecated = "use `config generate` instead."
	rootCmd.AddCommand(generateConfigCmd)

	// ./gitea-runner cache-server
	var cacheArgs cacheServerArgs
	cacheCmd := &cobra.Command{
		Use:   "cache-server",
		Short: "Start a cache server for the cache action",
		Args:  cobra.MaximumNArgs(0),
		RunE:  runCacheServer(&configFile, &cacheArgs),
	}
	cacheCmd.Flags().StringVarP(&cacheArgs.Dir, "dir", "d", "", "Cache directory")
	cacheCmd.Flags().StringVarP(&cacheArgs.Host, "host", "s", "", "Host of the cache server")
	cacheCmd.Flags().Uint16VarP(&cacheArgs.Port, "port", "p", 0, "Port of the cache server")
	rootCmd.AddCommand(cacheCmd)
	rootCmd.AddCommand(loadGuestCmd(ctx))
	rootCmd.AddCommand(loadDoctorCmd(ctx, &configFile))
	rootCmd.AddCommand(loadStatusCmd(ctx, &configFile))
	rootCmd.AddCommand(loadCleanupCmd(ctx, &configFile))
	rootCmd.AddCommand(loadImageCmd(ctx, &configFile))
	rootCmd.AddCommand(loadServiceCmd(&configFile))
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the runner version",
		Args:  cobra.NoArgs,
		Run: func(command *cobra.Command, _ []string) {
			fmt.Fprintf(command.OutOrStdout(), "gitea-runner-lume %s\n", ver.Version())
		},
	})

	// hide completion command
	rootCmd.CompletionOptions.HiddenDefaultCmd = true
	return rootCmd
}
