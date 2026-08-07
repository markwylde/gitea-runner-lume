//go:build !darwin

package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

func loadServiceCmd(_ *string) *cobra.Command {
	return &cobra.Command{Use: "service", Short: "Manage the macOS user LaunchAgent", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		return errors.New("service management requires macOS")
	}}
}

func validateInstalledService(string, bool) error {
	return errors.New("service validation requires macOS")
}

func requireServiceStopped() error {
	return errors.New("live guest probe requires macOS")
}
