// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/guestagent"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

func loadInitCmd(configFile *string) *cobra.Command {
	var profileName, imageName, storageName, storagePath string
	var noRegister bool
	var registration registerArgs
	command := &cobra.Command{
		Use:   "init",
		Short: "Initialize a Lume runner configuration and controller keys",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			file := *configFile
			if file == "" {
				file = defaultConfigFilePath()
			}
			if file == "" {
				return errors.New("resolve home directory for default config path")
			}
			var err error
			file, err = filepath.Abs(file)
			if err != nil {
				return fmt.Errorf("resolve config path: %w", err)
			}
			if err := ensurePrivateDirectory(filepath.Dir(file)); err != nil {
				return fmt.Errorf("configuration directory: %w", err)
			}

			if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
				content, generateErr := generateLumeStarter(profileName, imageName, storageName, storagePath, filepath.Dir(file))
				if generateErr != nil {
					return generateErr
				}
				if writeErr := config.WriteFile(file, content); writeErr != nil {
					return writeErr
				}
				fmt.Fprintf(command.OutOrStdout(), "created Lume configuration %s\n", file)
			} else if err != nil {
				return fmt.Errorf("inspect config file %q: %w", file, err)
			} else {
				fmt.Fprintf(command.OutOrStdout(), "using existing configuration %s\n", file)
			}

			cfg, err := config.LoadDefault(file)
			if err != nil {
				return fmt.Errorf("load initialized configuration: %w", err)
			}
			if !cfg.Lume.Enabled {
				return errors.New("existing configuration does not enable Lume")
			}
			if !filepath.IsAbs(cfg.Runner.File) {
				registrationFile := filepath.Join(filepath.Dir(file), ".runner")
				if err := config.SetValue(file, "runner.file", registrationFile); err != nil {
					return fmt.Errorf("migrate runner registration path: %w", err)
				}
				cfg, err = config.LoadDefault(file)
				if err != nil {
					return fmt.Errorf("reload migrated configuration: %w", err)
				}
				fmt.Fprintf(command.OutOrStdout(), "set runner registration path to %s\n", registrationFile)
			}
			if err := ensurePrivateDirectory(filepath.Join(filepath.Dir(file), "images")); err != nil {
				return fmt.Errorf("image manifest directory: %w", err)
			}

			if err := ensureEd25519KeyPair(cfg.Lume.SSHIdentityFile, cfg.Lume.SSHIdentityFile+".pub"); err != nil {
				return fmt.Errorf("controller SSH key: %w", err)
			}
			signingPrivate := filepath.Join(filepath.Dir(cfg.Lume.ImageSigningPublicKeyFile), "image-signing.key")
			if err := ensureEd25519KeyPair(signingPrivate, cfg.Lume.ImageSigningPublicKeyFile); err != nil {
				return fmt.Errorf("image signing key: %w", err)
			}

			fmt.Fprintln(command.OutOrStdout(), "controller and image-signing keys are ready")
			if !noRegister {
				if err := initializeRegistration(command.Context(), command, file, &registration); err != nil {
					return err
				}
				fmt.Fprintln(command.OutOrStdout(), "the runner will remain offline until image setup is complete and the service is installed")
			}
			fmt.Fprintf(command.OutOrStdout(), "next: gitea-runner-lume image create --profile %s --ipsw latest --unattended tahoe\n", profileName)
			fmt.Fprintln(command.OutOrStdout(), "after creating the VM, complete the guest bootstrap in docs/lume-setup.md before image adopt")
			return nil
		},
	}
	command.Flags().StringVar(&profileName, "profile", "xcode-16", "Lume profile and runner label suffix")
	command.Flags().StringVar(&imageName, "image", "grl-xcode-16", "Lume base VM name")
	command.Flags().StringVar(&storageName, "storage", "home", "Lume storage name")
	command.Flags().StringVar(&storagePath, "storage-path", "", "absolute Lume storage path (defaults to ~/.lume)")
	command.Flags().BoolVar(&noRegister, "no-register", false, "initialize local files without registering with Gitea")
	command.Flags().StringVar(&registration.InstanceAddr, "instance", "", "Gitea instance address")
	command.Flags().StringVar(&registration.Token, "token", "", "runner token (prefer --token-file because process arguments are observable)")
	command.Flags().StringVar(&registration.TokenFile, "token-file", "", "owner-only file containing the runner registration token")
	command.Flags().StringVar(&registration.RunnerName, "name", "", "runner name (defaults to the hostname)")
	return command
}

func initializeRegistration(ctx context.Context, command *cobra.Command, configFile string, args *registerArgs) error {
	cfg, err := config.LoadDefault(configFile)
	if err != nil {
		return err
	}
	if info, statErr := os.Lstat(cfg.Runner.File); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("runner registration must be an owner-only regular file")
		}
		fmt.Fprintf(command.OutOrStdout(), "using existing runner registration %s\n", cfg.Runner.File)
		return nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect runner registration: %w", statErr)
	}

	reader := bufio.NewReader(command.InOrStdin())
	instance := strings.TrimSpace(args.InstanceAddr)
	if instance == "" {
		instance, err = promptLine(command, reader, "Gitea instance URL: ")
		if err != nil {
			return err
		}
	}
	token, err := registrationToken(command, reader, args)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(args.RunnerName)
	if name == "" {
		hostname, _ := os.Hostname()
		name, err = promptLine(command, reader, fmt.Sprintf("Runner name [%s]: ", hostname))
		if err != nil {
			return err
		}
		if name == "" {
			name = hostname
		}
	}
	inputs := &registerInputs{
		InstanceAddr: instance,
		Token:        token,
		RunnerName:   name,
		Labels:       cfg.Runner.Labels,
	}
	if err := inputs.validate(); err != nil {
		return fmt.Errorf("invalid registration input: %w", err)
	}
	if err := cfg.ValidateLumeLabels(inputs.Labels); err != nil {
		return fmt.Errorf("invalid Lume runner labels: %w", err)
	}
	if err := doRegister(ctx, cfg, inputs); err != nil {
		return fmt.Errorf("register runner: %w", err)
	}
	fmt.Fprintf(command.OutOrStdout(), "registered runner %s with %s\n", name, instance)
	return nil
}

func registrationToken(command *cobra.Command, reader *bufio.Reader, args *registerArgs) (string, error) {
	if args.TokenFile != "" || args.Token != "" || os.Getenv(registerTokenEnvVar) != "" {
		inputs, err := initInputs(args)
		if err != nil {
			return "", err
		}
		return inputs.Token, nil
	}
	fmt.Fprint(command.ErrOrStderr(), "Runner registration token: ")
	if file, ok := command.InOrStdin().(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		value, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(command.ErrOrStderr())
		if err != nil {
			return "", fmt.Errorf("read runner registration token: %w", err)
		}
		if token := strings.TrimSpace(string(value)); token != "" {
			return token, nil
		}
		return "", errors.New("runner registration token is empty")
	}
	value, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read runner registration token: %w", err)
	}
	if token := strings.TrimSpace(value); token != "" {
		return token, nil
	}
	return "", errors.New("runner registration token is empty")
}

func promptLine(command *cobra.Command, reader *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(command.ErrOrStderr(), prompt)
	value, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read setup input: %w", err)
	}
	return strings.TrimSpace(value), nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("must be a directory accessible only by its owner")
	}
	return nil
}

func ensureEd25519KeyPair(privatePath, publicPath string) error {
	privateInfo, privateErr := os.Lstat(privatePath)
	publicInfo, publicErr := os.Lstat(publicPath)
	privateExists := privateErr == nil
	publicExists := publicErr == nil
	if privateErr != nil && !errors.Is(privateErr, os.ErrNotExist) {
		return privateErr
	}
	if publicErr != nil && !errors.Is(publicErr, os.ErrNotExist) {
		return publicErr
	}
	if privateExists != publicExists {
		return errors.New("key pair is incomplete; restore the missing file or remove both files")
	}
	if privateExists {
		if !privateInfo.Mode().IsRegular() || privateInfo.Mode().Perm()&0o077 != 0 {
			return errors.New("private key must be an owner-only regular file")
		}
		if !publicInfo.Mode().IsRegular() || publicInfo.Mode().Perm()&0o022 != 0 {
			return errors.New("public key must be a regular file not writable by group or others")
		}
		privateKey, err := guestagent.LoadEd25519PrivateKey(privatePath)
		if err != nil {
			return err
		}
		publicKey, err := guestagent.LoadEd25519PublicKey(publicPath)
		if err != nil {
			return err
		}
		if !bytes.Equal(privateKey.Public().(ed25519.PublicKey), publicKey) {
			return errors.New("private and public keys do not match")
		}
		return nil
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privateBlock, err := ssh.MarshalPrivateKey(privateKey, "gitea-runner-lume")
	if err != nil {
		return err
	}
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(privatePath), 0o700); err != nil {
		return err
	}
	if err := writeNewFile(privatePath, pem.EncodeToMemory(privateBlock), 0o600); err != nil {
		return err
	}
	if err := writeNewFile(publicPath, ssh.MarshalAuthorizedKey(sshPublicKey), 0o644); err != nil {
		_ = os.Remove(privatePath)
		return err
	}
	return nil
}

func writeNewFile(path string, content []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
