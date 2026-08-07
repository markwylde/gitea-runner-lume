// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"fmt"
	"os"

	"gitea.com/gitea/runner/internal/pkg/guestagent"
	"gitea.com/gitea/runner/internal/pkg/guestproto"
	"gitea.com/gitea/runner/internal/pkg/ver"

	"github.com/spf13/cobra"
)

const (
	guestHostPublicKeyPath = "/etc/gitea-runner-lume/host.pub"
	guestPrivateKeyPath    = "/etc/gitea-runner-lume/guest.key"
)

type guestArgs struct {
	Root           string
	InstallationID string
	LeaseID        string
	WorkerID       string
	TaskID         int64
	Nonce          string
	Revision       string
}

func loadGuestCmd(ctx context.Context) *cobra.Command {
	var args guestArgs
	command := &cobra.Command{
		Use:    "guest",
		Short:  "Run the trusted one-job Lume guest agent",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if args.Revision != ver.Version() {
				return fmt.Errorf("guest executable revision mismatch")
			}
			expectedRoot := "/private/var/tmp/gitea-runner-lume/" + args.WorkerID
			if args.Root != expectedRoot {
				return fmt.Errorf("guest root does not match the worker identity")
			}
			if err := guestagent.PrepareWorkspace(args.Root); err != nil {
				return fmt.Errorf("create guest workspace: %w", err)
			}
			defer os.RemoveAll(args.Root)
			hostPublicKey, err := guestagent.LoadEd25519PublicKey(guestHostPublicKeyPath)
			if err != nil {
				return fmt.Errorf("load pinned host identity: %w", err)
			}
			guestPrivateKey, err := guestagent.LoadEd25519PrivateKey(guestPrivateKeyPath)
			if err != nil {
				return fmt.Errorf("load guest identity: %w", err)
			}
			expected := guestproto.Hello{
				InstallationID: args.InstallationID, LeaseID: args.LeaseID,
				WorkerID: args.WorkerID, TaskID: args.TaskID, Nonce: args.Nonce,
				Revision: args.Revision,
			}
			server, err := guestagent.NewServer(args.Root, os.Stdin, os.Stdout, expected, hostPublicKey, guestPrivateKey)
			if err != nil {
				return err
			}
			return server.Serve(ctx)
		},
	}
	flags := command.Flags()
	flags.StringVar(&args.Root, "root", "", "Absolute one-job guest workspace root")
	flags.StringVar(&args.InstallationID, "installation", "", "Controller installation identity")
	flags.StringVar(&args.LeaseID, "lease", "", "Capacity lease identity")
	flags.StringVar(&args.WorkerID, "worker", "", "Worker identity")
	flags.Int64Var(&args.TaskID, "task", 0, "Gitea task identity")
	flags.StringVar(&args.Nonce, "nonce", "", "One-job session nonce")
	flags.StringVar(&args.Revision, "revision", "", "Host/guest executable revision")
	_ = command.MarkFlagRequired("root")
	_ = command.MarkFlagRequired("installation")
	_ = command.MarkFlagRequired("lease")
	_ = command.MarkFlagRequired("worker")
	_ = command.MarkFlagRequired("task")
	_ = command.MarkFlagRequired("nonce")
	_ = command.MarkFlagRequired("revision")
	return command
}
