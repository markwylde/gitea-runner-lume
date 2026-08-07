// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/guestagent"
	"gitea.com/gitea/runner/internal/pkg/guestbootstrap"
	"gitea.com/gitea/runner/internal/pkg/lume"
	"gitea.com/gitea/runner/internal/pkg/ver"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	bootstrapNodeVersion = "24.19.0"
	bootstrapNodeSHA256  = "8294b7aa9b03997481c06babf1e8b270c859358f27da57a11509afe537ac381d"
)

var bootstrapHostnamePattern = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type imageBootstrapOptions struct {
	profile      string
	password     string
	passwordFile string
	agent        string
}

func loadImageBootstrapCmd(ctx context.Context, configFile *string) *cobra.Command {
	var options imageBootstrapOptions
	command := &cobra.Command{
		Use:   "bootstrap",
		Short: "Install and harden the runner guest in a created base VM",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			cfg, err := loadCommandConfig(command, configFile)
			if err != nil {
				return err
			}
			profile, ok := cfg.Lume.Profiles[options.profile]
			if !ok {
				return fmt.Errorf("unknown Lume profile %q", options.profile)
			}
			password, err := bootstrapPassword(options)
			if err != nil {
				return err
			}
			return bootstrapImage(ctx, command.OutOrStdout(), cfg, profile, options.agent, password)
		},
	}
	command.Flags().StringVar(&options.profile, "profile", "", "configured profile to bootstrap")
	command.Flags().StringVar(&options.passwordFile, "password-file", "", "owner-only file containing the temporary unattended VM password")
	command.Flags().StringVar(&options.password, "password", "", "temporary unattended VM password (observable; prefer --password-file)")
	command.Flags().StringVar(&options.agent, "agent", "", "runner binary to install in the guest (defaults to this executable)")
	_ = command.MarkFlagRequired("profile")
	return command
}

func bootstrapPassword(options imageBootstrapOptions) (string, error) {
	if options.password != "" && options.passwordFile != "" {
		return "", errors.New("pass either --password or --password-file, not both")
	}
	if options.passwordFile == "" {
		if options.password == "" {
			return "lume", nil
		}
		return options.password, nil
	}
	info, err := os.Lstat(options.passwordFile)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("bootstrap password file must be an owner-only regular file")
	}
	value, err := os.ReadFile(options.passwordFile)
	if err != nil {
		return "", err
	}
	password := strings.TrimSpace(string(value))
	if password == "" || len(password) > 1024 || strings.ContainsAny(password, "\r\n\x00") {
		return "", errors.New("bootstrap password is invalid")
	}
	return password, nil
}

func bootstrapImage(ctx context.Context, output io.Writer, cfg *config.Config, profile config.LumeProfile, agentPath, password string) error {
	if cfg.Lume.SSHUser != "lume" {
		return errors.New("image bootstrap currently requires Lume's unattended guest user named lume")
	}
	if agentPath == "" {
		var err error
		agentPath, err = os.Executable()
		if err != nil {
			return err
		}
	}
	agentInfo, err := os.Lstat(agentPath)
	if err != nil {
		return fmt.Errorf("inspect local runner agent: %w", err)
	}
	if !agentInfo.Mode().IsRegular() || agentInfo.Size() < 1 || agentInfo.Size() > 128*1024*1024 {
		return errors.New("local runner agent must be a bounded regular file")
	}
	agent, err := os.ReadFile(agentPath)
	if err != nil {
		return fmt.Errorf("read local runner agent: %w", err)
	}
	hostPublicKey, err := os.ReadFile(cfg.Lume.SSHIdentityFile + ".pub")
	if err != nil {
		return fmt.Errorf("read controller public key: %w", err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey(hostPublicKey); err != nil {
		return fmt.Errorf("parse controller public key: %w", err)
	}
	hostPrivateKey, err := guestagent.LoadEd25519PrivateKey(cfg.Lume.SSHIdentityFile)
	if err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(ed25519.PrivateKey(hostPrivateKey))
	if err != nil {
		return err
	}

	provider, err := lume.NewProvider(cfg.Lume.Executable, cfg.Lume.Storage, cfg.Runner.Timeout, nil)
	if err != nil {
		return err
	}
	vm, err := provider.Get(ctx, profile.Image)
	if err != nil {
		return err
	}
	if vm.State != lume.StateStopped || vm.Storage != cfg.Lume.Storage {
		return errors.New("base VM must be stopped in configured storage before bootstrap")
	}

	fmt.Fprintf(output, "starting base VM %s for bootstrap\n", profile.Image)
	process, err := provider.StartManaged(ctx, profile.Image)
	if err != nil {
		return err
	}
	defer func() {
		_ = provider.Stop(context.Background(), profile.Image, false)
		_ = process.Kill()
		_ = process.Wait()
	}()

	vm, err = waitForBootstrapGuest(ctx, provider, profile)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "guest SSH is ready at %s\n", vm.IPAddress)
	passwordClient, err := guestbootstrap.NewClient(vm.IPAddress, int(cfg.Lume.SSHPort), cfg.Lume.SSHUser, cfg.Lume.ConnectTimeout, ssh.Password(password))
	if err != nil {
		return err
	}
	keyClient, err := guestbootstrap.NewClient(vm.IPAddress, int(cfg.Lume.SSHPort), cfg.Lume.SSHUser, cfg.Lume.ConnectTimeout, ssh.PublicKeys(signer))
	if err != nil {
		return err
	}

	pinnedHostKey, err := loadPinnedHostKey(cfg.Lume.KnownHostsFile, profile.Image)
	if err != nil {
		return err
	}
	giteaHostname, guestHostEntry, err := resolveGuestGiteaHost(ctx, cfg, keyClient, passwordClient)
	if err != nil {
		return err
	}

	// A rerun after hardening cannot use the removed password, so prove the
	// installed controller key first and only run mutation when it is absent.
	_, keyErr := keyClient.Run(ctx, bootstrapVerificationCommand(ver.Version(), giteaHostname), nil)
	if presented := keyClient.HostKey(); pinnedHostKey != nil && (presented == nil || !bytes.Equal(pinnedHostKey.Marshal(), presented.Marshal())) {
		return errors.New("guest SSH host key does not match the pinned base image identity")
	}
	if keyErr != nil {
		fmt.Fprintln(output, "installing version-matched guest agent")
		if _, err := passwordClient.Run(ctx, "umask 077; cat > /tmp/gitea-runner-lume.bootstrap", agent); err != nil {
			return err
		}
		if _, err := passwordClient.Run(ctx, "umask 077; cat > /tmp/gitea-runner-lume.host.pub", hostPublicKey); err != nil {
			return err
		}
		if len(guestHostEntry) > 0 {
			fmt.Fprintf(output, "installing guest resolution for %s\n", giteaHostname)
			if _, err := passwordClient.Run(ctx, "umask 077; cat > /tmp/gitea-runner-lume.hosts", guestHostEntry); err != nil {
				return err
			}
		}
		if _, err := passwordClient.Run(ctx, "umask 077; cat > /tmp/gitea-runner-lume.bootstrap.sh", []byte(bootstrapScript(ver.Version(), giteaHostname))); err != nil {
			return err
		}
		fmt.Fprintln(output, "installing Command Line Tools and verified Node runtime; this can take several minutes")
		bootstrapOutput, err := passwordClient.RunWithOutput(ctx, "sudo -S -p '' /bin/bash /tmp/gitea-runner-lume.bootstrap.sh", []byte(password+"\n"), output)
		if err != nil {
			return err
		}
		if !bytes.Contains(bootstrapOutput, []byte("GRL_ROOT_BOOTSTRAP_OK")) {
			return errors.New("guest root bootstrap did not report successful hardening")
		}
		fmt.Fprintln(output, "waiting for the hardened guest to shut down cleanly")
		if err := waitForBootstrapStop(ctx, provider, profile.Image, profile.CleanupTimeout); err != nil {
			return err
		}
		_ = process.Wait()
		fmt.Fprintln(output, "restarting the guest to verify persisted key-only access")
		process, err = provider.StartManaged(ctx, profile.Image)
		if err != nil {
			return err
		}
		vm, err = waitForBootstrapGuest(ctx, provider, profile)
		if err != nil {
			return err
		}
		passwordClient, err = guestbootstrap.NewClient(vm.IPAddress, int(cfg.Lume.SSHPort), cfg.Lume.SSHUser, cfg.Lume.ConnectTimeout, ssh.Password(password))
		if err != nil {
			return err
		}
		keyClient, err = guestbootstrap.NewClient(vm.IPAddress, int(cfg.Lume.SSHPort), cfg.Lume.SSHUser, cfg.Lume.ConnectTimeout, ssh.PublicKeys(signer))
		if err != nil {
			return err
		}
	}

	verification, err := keyClient.Run(ctx, bootstrapVerificationCommand(ver.Version(), giteaHostname), nil)
	if err != nil {
		return fmt.Errorf("verify key-only guest bootstrap: %w", err)
	}
	guestPublicKey, err := parseBootstrapVerification(verification)
	if err != nil {
		return err
	}
	if _, err := passwordClient.Run(ctx, "true", nil); err == nil {
		return errors.New("guest still accepts the temporary unattended password")
	}
	hostKey := keyClient.HostKey()
	if hostKey == nil {
		return errors.New("guest did not present an SSH host key")
	}
	if passwordHostKey := passwordClient.HostKey(); passwordHostKey != nil && !bytes.Equal(passwordHostKey.Marshal(), hostKey.Marshal()) {
		return errors.New("guest SSH host key changed between bootstrap authentication methods")
	}
	if err := writePublicIdentity(cfg.Lume.GuestPublicKeyFile, guestPublicKey); err != nil {
		return fmt.Errorf("write guest public key: %w", err)
	}
	knownHosts := []byte(knownhosts.Line([]string{profile.Image}, hostKey) + "\n")
	if err := writePublicIdentity(cfg.Lume.KnownHostsFile, knownHosts); err != nil {
		return fmt.Errorf("write pinned SSH host key: %w", err)
	}

	if err := provider.Stop(ctx, profile.Image, false); err != nil {
		return err
	}
	fmt.Fprintf(output, "bootstrapped and stopped base VM %s\n", profile.Image)
	fmt.Fprintf(output, "next: gitea-runner-lume image adopt --profile %s --signing-key-file %s\n", profileNameForImage(cfg, profile.Image), filepath.Join(filepath.Dir(cfg.Lume.ImageSigningPublicKeyFile), "image-signing.key"))
	return nil
}

func loadPinnedHostKey(path, image string) (ssh.PublicKey, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	remainder := data
	for len(bytes.TrimSpace(remainder)) > 0 {
		_, hosts, key, _, rest, parseErr := ssh.ParseKnownHosts(remainder)
		if parseErr != nil {
			return nil, fmt.Errorf("parse pinned SSH host key: %w", parseErr)
		}
		for _, host := range hosts {
			if host == image {
				return key, nil
			}
		}
		remainder = rest
	}
	return nil, fmt.Errorf("pinned SSH host key has no entry for %s", image)
}

func waitForBootstrapGuest(ctx context.Context, provider *lume.Provider, profile config.LumeProfile) (lume.VM, error) {
	deadline := time.Now().Add(profile.BootTimeout)
	for time.Now().Before(deadline) {
		vm, err := provider.Get(ctx, profile.Image)
		if err == nil && vm.State == lume.StateRunning && vm.IPAddress != "" && vm.SSHAvailable {
			return vm, nil
		}
		select {
		case <-ctx.Done():
			return lume.VM{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return lume.VM{}, errors.New("timed out waiting for guest SSH readiness")
}

func waitForBootstrapStop(ctx context.Context, provider *lume.Provider, image string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		vm, err := provider.Get(ctx, image)
		if err == nil && vm.State == lume.StateStopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New("timed out waiting for the bootstrapped guest to stop")
}

func profileNameForImage(cfg *config.Config, image string) string {
	for name, profile := range cfg.Lume.Profiles {
		if profile.Image == image {
			return name
		}
	}
	return image
}

func parseBootstrapVerification(output []byte) ([]byte, error) {
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	if len(lines) < 4 || string(lines[0]) != "GRL_BOOTSTRAP_OK" {
		return nil, errors.New("guest bootstrap verification returned invalid output")
	}
	guestKey := append([]byte(nil), lines[1]...)
	guestKey = append(guestKey, '\n')
	if _, _, _, _, err := ssh.ParseAuthorizedKey(guestKey); err != nil {
		return nil, fmt.Errorf("parse exported guest public key: %w", err)
	}
	return guestKey, nil
}

func writePublicIdentity(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, content) {
			return nil
		}
		return errors.New("existing public identity does not match the bootstrapped guest")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return writeNewFile(path, content, 0o644)
}

func bootstrapVerificationCommand(version, giteaHostname string) string {
	return "set -eu; export PATH='/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin'; " +
		"test \"$(id -u)\" -gt 0; " +
		"if id -Gn | tr ' ' '\\n' | grep -qx admin; then echo 'runner user is still an administrator' >&2; exit 1; fi; " +
		"test \"$(/usr/local/bin/gitea-runner-lume version)\" = 'gitea-runner-lume " + version + "'; " +
		"test \"$(node --version)\" = 'v" + bootstrapNodeVersion + "'; " +
		"xcrun --find git >/dev/null; " +
		guestResolutionCommand(giteaHostname) + "; " +
		"printf 'GRL_BOOTSTRAP_OK\\n'; cat /etc/gitea-runner-lume/guest.key.pub; node --version; git --version"
}

func bootstrapScript(version, giteaHostname string) string {
	return `set -euo pipefail
export PATH="/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
echo 'bootstrap: installing guest agent and identities'
test "$(id -u lume)" -gt 0
install -d -o root -g wheel -m 0755 /usr/local/bin
install -o root -g wheel -m 0755 /tmp/gitea-runner-lume.bootstrap /usr/local/bin/gitea-runner-lume
test "$(/usr/local/bin/gitea-runner-lume version)" = "gitea-runner-lume ` + version + `"

install -d -o lume -g staff -m 0700 /etc/gitea-runner-lume
install -o lume -g staff -m 0644 /tmp/gitea-runner-lume.host.pub /etc/gitea-runner-lume/host.pub
if [ ! -f /etc/gitea-runner-lume/guest.key ]; then
  sudo -u lume ssh-keygen -q -t ed25519 -N '' -f /etc/gitea-runner-lume/guest.key
fi
chmod 0600 /etc/gitea-runner-lume/guest.key
chmod 0644 /etc/gitea-runner-lume/guest.key.pub

install -d -o lume -g staff -m 0700 /Users/lume/.ssh
touch /Users/lume/.ssh/authorized_keys
chown lume:staff /Users/lume/.ssh/authorized_keys
chmod 0600 /Users/lume/.ssh/authorized_keys
host_key="$(cat /tmp/gitea-runner-lume.host.pub)"
grep -Fqx "$host_key" /Users/lume/.ssh/authorized_keys || printf '%s\n' "$host_key" >> /Users/lume/.ssh/authorized_keys

if ! xcrun --find git >/dev/null 2>&1; then
	 echo 'bootstrap: installing Apple Command Line Tools'
  marker=/tmp/.com.apple.dt.CommandLineTools.installondemand.in-progress
  touch "$marker"
  trap 'rm -f "$marker"' EXIT
  label="$(softwareupdate -l 2>&1 | awk -F 'Label: ' '/Label: Command Line Tools for Xcode/{gsub(/\r/, "", $2); print $2}' | tail -n 1)"
  case "$label" in
    'Command Line Tools for Xcode '[A-Za-z0-9.-]*) ;;
    *) echo 'bootstrap: no valid Command Line Tools update was found' >&2; exit 1 ;;
  esac
  softwareupdate --install "$label" --verbose
  rm -f "$marker"
  trap - EXIT
fi
xcrun --find git >/dev/null

echo 'bootstrap: installing verified Node runtime'
node_version=` + bootstrapNodeVersion + `
node_archive="node-v${node_version}-darwin-arm64.tar.gz"
node_url="https://nodejs.org/dist/v${node_version}/${node_archive}"
node_tmp="$(mktemp -d)"
trap 'rm -rf "$node_tmp"' EXIT
curl -fsSL "$node_url" -o "$node_tmp/$node_archive"
printf '` + bootstrapNodeSHA256 + `  %s\n' "$node_tmp/$node_archive" | shasum -a 256 -c -
tar -xzf "$node_tmp/$node_archive" -C "$node_tmp"
install -d -o root -g wheel -m 0755 /usr/local/lib/nodejs
rm -rf "/usr/local/lib/nodejs/node-v${node_version}-darwin-arm64"
cp -R "$node_tmp/node-v${node_version}-darwin-arm64" /usr/local/lib/nodejs/
chown -R root:wheel "/usr/local/lib/nodejs/node-v${node_version}-darwin-arm64"
for tool in node npm npx corepack; do
  ln -sfn "/usr/local/lib/nodejs/node-v${node_version}-darwin-arm64/bin/$tool" "/usr/local/bin/$tool"
done
test "$(node --version)" = "v${node_version}"

if [ -f /tmp/gitea-runner-lume.hosts ]; then
  echo 'bootstrap: installing scoped Gitea hostname resolution'
  grep -Fqx "$(cat /tmp/gitea-runner-lume.hosts)" /etc/hosts || cat /tmp/gitea-runner-lume.hosts >> /etc/hosts
fi
dscacheutil -flushcache
` + guestResolutionCommand(giteaHostname) + `

if ! id grl-maintenance >/dev/null 2>&1; then
  maintenance_password="$(openssl rand -base64 48)"
  sysadminctl -addUser grl-maintenance -fullName 'Gitea Runner Maintenance' -password "$maintenance_password" -admin
  dscl . -create /Users/grl-maintenance IsHidden 1
fi
dseditgroup -o checkmember -m grl-maintenance admin | grep -q 'yes'

cat > /etc/ssh/sshd_config.d/100-gitea-runner-lume.conf <<'SSHD'
PasswordAuthentication no
KbdInteractiveAuthentication no
ChallengeResponseAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
AllowUsers lume
SSHD
chmod 0644 /etc/ssh/sshd_config.d/100-gitea-runner-lume.conf
/usr/sbin/sshd -t
dseditgroup -o edit -a lume -t user com.apple.access_ssh
dseditgroup -o edit -d lume -t user admin
if id -Gn lume | tr ' ' '\n' | grep -qx admin; then
  echo 'bootstrap: failed to remove runner user from admin group' >&2
  exit 1
fi
test -f /etc/ssh/sshd_config.d/100-gitea-runner-lume.conf
defaults delete /Library/Preferences/com.apple.loginwindow autoLoginUser >/dev/null 2>&1 || true
rm -f /etc/kcpassword
dscl . -delete /Users/lume dsAttrTypeNative:ShadowHashData >/dev/null 2>&1 || true
dscl . -delete /Users/lume AuthenticationAuthority >/dev/null 2>&1 || true
rm -f /tmp/gitea-runner-lume.bootstrap /tmp/gitea-runner-lume.host.pub /tmp/gitea-runner-lume.hosts
rm -f /tmp/gitea-runner-lume.bootstrap.sh
sync
echo 'GRL_ROOT_BOOTSTRAP_OK'
(sleep 5; /sbin/shutdown -h now) >/dev/null 2>&1 &
`
}

func resolveGuestGiteaHost(ctx context.Context, cfg *config.Config, clients ...*guestbootstrap.Client) (string, []byte, error) {
	registration, err := config.LoadRegistration(cfg.Runner.File)
	if err != nil {
		return "", nil, fmt.Errorf("load runner registration for guest networking: %w", err)
	}
	hostname, isIP, err := registeredGiteaHostname(registration.Address)
	if err != nil {
		return "", nil, err
	}
	if isIP {
		return hostname, nil, nil
	}
	probe := guestResolutionCommand(hostname)
	for _, client := range clients {
		if _, probeErr := client.Run(ctx, probe, nil); probeErr == nil {
			return hostname, nil, nil
		}
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return "", nil, fmt.Errorf("resolve registered Gitea hostname on controller: %w", err)
	}
	var selected net.IP
	for _, address := range addresses {
		ip := address.IP
		if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
			continue
		}
		if ip.To4() != nil {
			selected = ip.To4()
			break
		}
		if selected == nil {
			selected = ip
		}
	}
	if selected == nil {
		return "", nil, errors.New("registered Gitea hostname has no usable controller address")
	}
	return hostname, []byte(selected.String() + " " + hostname + "\n"), nil
}

func registeredGiteaHostname(address string) (string, bool, error) {
	instance, err := url.Parse(address)
	if err != nil {
		return "", false, fmt.Errorf("parse registered Gitea address: %w", err)
	}
	hostname := strings.ToLower(strings.TrimSuffix(instance.Hostname(), "."))
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.String(), true, nil
	}
	if len(hostname) > 253 || !bootstrapHostnamePattern.MatchString(hostname) {
		return "", false, errors.New("registered Gitea address has an invalid DNS hostname")
	}
	return hostname, false, nil
}

func guestResolutionCommand(hostname string) string {
	if net.ParseIP(hostname) != nil {
		return "true"
	}
	return "dscacheutil -q host -a name '" + hostname + "' | grep -q 'ip_address:'"
}
