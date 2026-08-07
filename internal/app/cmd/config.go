// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gitea.com/gitea/runner/internal/pkg/config"

	"github.com/spf13/cobra"
)

func loadConfigCmd(configFile *string) *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Generate, read and edit config files",
		Args:  cobra.MaximumNArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	configCmd.AddCommand(loadGenerateConfigCmd("generate"))
	configCmd.AddCommand(loadInitConfigCmd(configFile))

	configCmd.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Print the value of a config key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := resolveConfigFile(cmd, configFile)
			if err != nil {
				return err
			}
			value, err := config.GetValue(file, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), value)
			return nil
		},
	})

	for _, sub := range []struct {
		use   string
		short string
		edit  func(file, key string, values ...string) error
	}{
		{"set <key> <value>...", "Set the value of a config key", config.SetValue},
		{"add <key> <value>...", "Append values to a list config key", config.AddValue},
		{"remove <key> <value>...", "Remove values from a list config key", config.RemoveValue},
	} {
		valueCmd := &cobra.Command{
			Use:   sub.use,
			Short: sub.short,
			Args:  cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				file, err := resolveConfigFile(cmd, configFile)
				if err != nil {
					return err
				}
				return sub.edit(file, args[0], args[1:]...)
			},
		}
		valueCmd.Flags().SetInterspersed(false) // so a value such as `--cpus 2` is not parsed as a flag
		configCmd.AddCommand(valueCmd)
	}

	return configCmd
}

func loadInitConfigCmd(configFile *string) *cobra.Command {
	var force, lumeEnabled bool
	var profileName, imageName, storageName, storagePath string
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Write a minimal or Lume-backed config file",
		Long:  "Write a minimal config file, or discover Lume and write a secure profile skeleton with --lume.\nWithout --config it writes config.yaml in the working directory.",
		Args:  cobra.MaximumNArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, taken := *configFile, []string{*configFile}
			if file == "" {
				file, taken = defaultConfigFileNames[0], defaultConfigFileNames // any of them would shadow the new file
			}
			for _, name := range taken {
				if _, err := os.Stat(name); err == nil && !force {
					return fmt.Errorf("config file %q already exists, pass --force to overwrite it", name)
				}
			}
			content := []byte(config.Minimal)
			if lumeEnabled {
				generated, err := generateLumeStarter(profileName, imageName, storageName, storagePath)
				if err != nil {
					return err
				}
				content = generated
			}
			if err := config.WriteFile(file, content); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote config file %q\n", file)
			return nil
		},
	}
	initCmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite an existing config file")
	initCmd.Flags().BoolVar(&lumeEnabled, "lume", false, "write a Lume-backed macOS runner configuration")
	initCmd.Flags().StringVar(&profileName, "profile", "xcode-16", "Lume profile and runner label suffix")
	initCmd.Flags().StringVar(&imageName, "image", "grl-xcode-16", "Lume base VM name")
	initCmd.Flags().StringVar(&storageName, "storage", "home", "Lume storage name")
	initCmd.Flags().StringVar(&storagePath, "storage-path", "", "absolute Lume storage path (defaults to ~/.lume)")
	return initCmd
}

func generateLumeStarter(profile, image, storage, storagePath string) ([]byte, error) {
	identifier := regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	if !identifier.MatchString(profile) || !identifier.MatchString(image) || !identifier.MatchString(storage) {
		return nil, fmt.Errorf("profile, image, and storage must be lowercase Lume identifiers")
	}
	executable, err := exec.LookPath("lume")
	if err != nil {
		return nil, fmt.Errorf("find Lume executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	versionOutput, err := exec.Command(executable, "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("read Lume version: %w", err)
	}
	version := strings.TrimSpace(string(versionOutput))
	if version == "" || len(version) > 128 || strings.ContainsAny(version, "\r\n") {
		return nil, fmt.Errorf("Lume returned an invalid version")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if storagePath == "" {
		storagePath = filepath.Join(home, ".lume")
	}
	if !filepath.IsAbs(storagePath) {
		return nil, fmt.Errorf("storage path must be absolute")
	}
	identity := make([]byte, 16)
	if _, err := rand.Read(identity); err != nil {
		return nil, err
	}
	configDir := filepath.Join(home, ".config", "gitea-runner-lume")
	stateDir := filepath.Join(home, "Library", "Application Support", "gitea-runner-lume")
	content := fmt.Sprintf(`runner:
  capacity: 1
  labels:
    - %s:lume://%s

lume:
  enabled: true
  executable: %q
  storage: %q
  storage_path: %q
  state_dir: %q
  installation_id: %s
  max_running_vms: 1
  supported_versions: [%q]
  ssh_identity_file: %q
  known_hosts_file: %q
  guest_public_key_file: %q
  image_signing_public_key_file: %q
  ssh_user: lume
  guest_binary: /usr/local/bin/gitea-runner-lume
  ssh_port: 22
  connect_timeout: 2m
  profiles:
    %s:
      image: %s
      manifest: %q
      cpu: 4
      memory_gb: 8
      disk_gb: 100
      boot_timeout: 10m
      cleanup_timeout: 5m
`, profile, profile, executable, storage, storagePath, stateDir, hex.EncodeToString(identity), version,
		filepath.Join(configDir, "host.key"), filepath.Join(configDir, "known_hosts"), filepath.Join(configDir, "guest.pub"), filepath.Join(configDir, "image-signing.pub"), profile, image, filepath.Join(configDir, "images", profile+".json"))
	return []byte(content), nil
}

func loadGenerateConfigCmd(use string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: "Print the example config, which documents every option",
		Args:  cobra.MaximumNArgs(0),
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "%s", config.Example)
		},
	}
}

var defaultConfigFileNames = []string{"config.yaml", "config.yml"}

func resolveConfigFile(cmd *cobra.Command, configFile *string) (string, error) {
	if *configFile != "" {
		return *configFile, nil
	}

	var dirs []string
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}

	for _, dir := range dirs {
		for _, name := range defaultConfigFileNames {
			candidate := filepath.Join(dir, name)
			if stat, err := os.Stat(candidate); err == nil && !stat.IsDir() {
				fmt.Fprintf(cmd.ErrOrStderr(), "using config file %q\n", candidate)
				return candidate, nil
			}
		}
	}

	return "", fmt.Errorf("no %s found in %s, pass one with --config",
		strings.Join(defaultConfigFileNames, " or "), strings.Join(dirs, " or "))
}
