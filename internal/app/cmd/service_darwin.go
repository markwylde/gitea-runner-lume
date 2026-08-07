// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const serviceLabel = "net.gitea.runner-lume"

var launchAgentTemplate = template.Must(template.New("plist").Parse(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>{{.Label}}</string>
<key>ProgramArguments</key><array><string>{{.Binary}}</string><string>--config</string><string>{{.Config}}</string><string>daemon</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>ProcessType</key><string>Background</string>
<key>WorkingDirectory</key><string>{{.WorkingDirectory}}</string>
<key>StandardOutPath</key><string>{{.Stdout}}</string>
<key>StandardErrorPath</key><string>{{.Stderr}}</string>
</dict></plist>
`))

func loadServiceCmd(configFile *string) *cobra.Command {
	service := &cobra.Command{Use: "service", Short: "Manage the macOS user LaunchAgent", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	service.AddCommand(&cobra.Command{Use: "install", Args: cobra.NoArgs, Short: "Install the user LaunchAgent", RunE: func(command *cobra.Command, _ []string) error {
		configPath, err := resolveConfigFile(command, configFile)
		if err != nil {
			return err
		}
		configPath, err = filepath.Abs(configPath)
		if err != nil {
			return err
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		binary, err = filepath.EvalSymlinks(binary)
		if err != nil {
			return err
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		logDir := filepath.Join(home, "Library", "Logs", "gitea-runner-lume")
		if err := os.MkdirAll(logDir, 0o700); err != nil {
			return err
		}
		plist := servicePlistPath(home)
		workingDirectory, err := os.Getwd()
		if err != nil {
			return err
		}
		workingDirectory, err = filepath.EvalSymlinks(workingDirectory)
		if err != nil {
			return err
		}
		var content bytes.Buffer
		if err := launchAgentTemplate.Execute(&content, map[string]string{"Label": serviceLabel, "Binary": binary, "Config": configPath, "WorkingDirectory": workingDirectory, "Stdout": filepath.Join(logDir, "stdout.log"), "Stderr": filepath.Join(logDir, "stderr.log")}); err != nil {
			return err
		}
		if err := writeOwnerOnlyFile(plist, content.Bytes()); err != nil {
			return err
		}
		_ = launchctl("bootout", serviceDomain()+"/"+serviceLabel)
		if err := waitForServiceUnload(5*time.Second, func() error {
			return launchctl("print", serviceDomain()+"/"+serviceLabel)
		}); err != nil {
			return err
		}
		if err := launchctl("bootstrap", serviceDomain(), plist); err != nil {
			return err
		}
		fmt.Fprintf(command.OutOrStdout(), "installed and started %s\n", plist)
		return nil
	}})
	service.AddCommand(&cobra.Command{Use: "start", Args: cobra.NoArgs, Short: "Start the user LaunchAgent", RunE: func(_ *cobra.Command, _ []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if launchctl("print", serviceDomain()+"/"+serviceLabel) == nil {
			return launchctl("kickstart", "-k", serviceDomain()+"/"+serviceLabel)
		}
		return launchctl("bootstrap", serviceDomain(), servicePlistPath(home))
	}})
	service.AddCommand(&cobra.Command{Use: "stop", Args: cobra.NoArgs, Short: "Stop the user LaunchAgent", RunE: func(_ *cobra.Command, _ []string) error {
		if launchctl("print", serviceDomain()+"/"+serviceLabel) != nil {
			return nil
		}
		return launchctl("bootout", serviceDomain()+"/"+serviceLabel)
	}})
	service.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, Short: "Show LaunchAgent state", RunE: func(command *cobra.Command, _ []string) error {
		cmd := exec.Command("/bin/launchctl", "print", serviceDomain()+"/"+serviceLabel)
		cmd.Stdout = command.OutOrStdout()
		cmd.Stderr = command.ErrOrStderr()
		return cmd.Run()
	}})
	service.AddCommand(&cobra.Command{Use: "uninstall", Args: cobra.NoArgs, Short: "Unload and remove the user LaunchAgent", RunE: func(command *cobra.Command, _ []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		_ = launchctl("bootout", serviceDomain()+"/"+serviceLabel)
		if err := os.Remove(servicePlistPath(home)); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintln(command.OutOrStdout(), "service uninstalled; configuration, registration, and images were preserved")
		return nil
	}})
	return service
}

func serviceDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }
func servicePlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
}
func launchctl(args ...string) error {
	output, err := exec.Command("/bin/launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %v: %w: %s", args, err, string(output))
	}
	return nil
}

func waitForServiceUnload(timeout time.Duration, status func() error) error {
	deadline := time.Now().Add(timeout)
	for status() == nil {
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for previous service instance to unload")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}
func writeOwnerOnlyFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".new"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(temporary)
		return closeErr
	}
	return os.Rename(temporary, path)
}

type launchAgentDocument struct {
	Label                string            `json:"Label"`
	ProgramArguments     []string          `json:"ProgramArguments"`
	WorkingDirectory     string            `json:"WorkingDirectory"`
	StandardOutPath      string            `json:"StandardOutPath"`
	StandardErrorPath    string            `json:"StandardErrorPath"`
	EnvironmentVariables map[string]string `json:"EnvironmentVariables"`
}

func validateInstalledService(configFile string, requireLoaded bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := servicePlistPath(home)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("service plist must be a regular owner-only file, got mode %s", info.Mode())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return errors.New("service plist is not owned by the current user")
	}
	output, err := exec.Command("/usr/bin/plutil", "-convert", "json", "-o", "-", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("parse service plist: %w: %s", err, string(output))
	}
	var document launchAgentDocument
	if err := json.Unmarshal(output, &document); err != nil {
		return fmt.Errorf("decode service plist: %w", err)
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return err
	}
	resolvedConfig, err := filepath.Abs(configFile)
	if err != nil {
		return err
	}
	if err := validateLaunchAgentDocument(document, binary, resolvedConfig); err != nil {
		return err
	}
	if err := launchctl("print", serviceDomain()+"/"+serviceLabel); requireLoaded && err != nil {
		return fmt.Errorf("service is not loaded: %w", err)
	}
	return nil
}

func requireServiceStopped() error {
	if launchctl("print", serviceDomain()+"/"+serviceLabel) == nil {
		return errors.New("live guest probe requires the runner service to be stopped")
	}
	return nil
}

func validateLaunchAgentDocument(document launchAgentDocument, binary, configFile string) error {
	if document.Label != serviceLabel {
		return fmt.Errorf("unexpected service label %q", document.Label)
	}
	if len(document.EnvironmentVariables) != 0 {
		return errors.New("service plist must not contain environment variables")
	}
	if !reflect.DeepEqual(document.ProgramArguments, []string{binary, "--config", configFile, "daemon"}) {
		return errors.New("service arguments do not match the current executable and config")
	}
	if document.WorkingDirectory == "" || !filepath.IsAbs(document.WorkingDirectory) {
		return errors.New("service working directory must be absolute")
	}
	for _, logPath := range []string{document.StandardOutPath, document.StandardErrorPath} {
		if logPath == "" || !filepath.IsAbs(logPath) {
			return errors.New("service log paths must be absolute")
		}
	}
	return nil
}
