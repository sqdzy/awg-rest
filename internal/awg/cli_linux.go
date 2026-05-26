//go:build linux

package awg

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// CLIExecutor invokes amneziawg-tools binaries on a Linux host. It is the
// production-mode executor used by the node-agent.
//
// Requirements: `awg`, `awg-quick` available in PATH; either the kernel module
// `amneziawg` is loadable or `awg-quick` can fall back to amneziawg-go via
// WG_QUICK_USERSPACE_IMPLEMENTATION; node-agent must run as root or with
// CAP_NET_ADMIN.
type CLIExecutor struct {
	AwgBinary          string // default: "awg"
	AwgQuickBinary     string // default: "awg-quick"
	RenderedDir        string // default: "/etc/amnezia/rendered"
	BootstrapConfigDir string // default: "/etc/amnezia/bootstrap"
}

// NewCLIExecutor returns a CLI executor with sensible defaults.
func NewCLIExecutor() *CLIExecutor {
	return &CLIExecutor{
		AwgBinary:          "awg",
		AwgQuickBinary:     "awg-quick",
		RenderedDir:        "/etc/amnezia/rendered",
		BootstrapConfigDir: "/etc/amnezia/bootstrap",
	}
}

func (c *CLIExecutor) run(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// awg-quick may daemonize amneziawg-go; regular files avoid waiting on
	// stdout/stderr pipes inherited by that background process.
	stdoutFile, err := os.CreateTemp("", "awg-stdout-*")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(stdoutFile.Name())
	stderrFile, err := os.CreateTemp("", "awg-stderr-*")
	if err != nil {
		_ = stdoutFile.Close()
		return "", "", err
	}
	defer os.Remove(stderrFile.Name())

	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	runErr := cmd.Run()
	_ = stdoutFile.Close()
	_ = stderrFile.Close()

	stdoutBytes, readOutErr := os.ReadFile(stdoutFile.Name())
	if readOutErr != nil {
		return "", "", readOutErr
	}
	stderrBytes, readErrErr := os.ReadFile(stderrFile.Name())
	if readErrErr != nil {
		return "", "", readErrErr
	}
	stdout, stderr := string(stdoutBytes), string(stderrBytes)
	if runErr != nil {
		return stdout, stderr,
			fmt.Errorf("awg: %s %v failed: %w (stderr=%q)", name, args, runErr, stderr)
	}
	return stdout, stderr, nil
}

func (c *CLIExecutor) SyncConf(ctx context.Context, iface, config string) error {
	if err := validateInterfaceName(iface); err != nil {
		return err
	}
	config = c.preservePrivateKey(ctx, iface, config)
	if err := os.MkdirAll(c.RenderedDir, 0o700); err != nil {
		return fmt.Errorf("rendered dir: %w", err)
	}
	path := filepath.Join(c.RenderedDir, iface+".conf")
	if err := writeAtomic(path, []byte(config), 0o600); err != nil {
		return err
	}
	// `awg-quick strip` filters out PostUp/PostDown/etc., yielding a config
	// suitable for `awg syncconf`.
	stripped, _, err := c.run(ctx, c.AwgQuickBinary, "strip", path)
	if err != nil {
		return err
	}
	stripped = sanitizeSyncConf(stripped)
	tmp, err := os.CreateTemp(c.RenderedDir, iface+"-stripped-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(stripped); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_, _, err = c.run(ctx, c.AwgBinary, "syncconf", iface, tmp.Name())
	return err
}

func (c *CLIExecutor) preservePrivateKey(ctx context.Context, iface, config string) string {
	if InterfaceValue(config, "PrivateKey") != "" {
		return config
	}
	if current, _, err := c.run(ctx, c.AwgBinary, "showconf", iface); err == nil {
		config = PreserveInterfacePrivateKey(config, current)
		if InterfaceValue(config, "PrivateKey") != "" {
			return config
		}
	}
	if b, err := os.ReadFile(filepath.Join(c.bootstrapConfigDir(), iface+".conf")); err == nil {
		config = PreserveInterfacePrivateKey(config, string(b))
	}
	return config
}

func (c *CLIExecutor) bootstrapConfigDir() string {
	if c.BootstrapConfigDir != "" {
		return c.BootstrapConfigDir
	}
	return "/etc/amnezia/bootstrap"
}

func (c *CLIExecutor) SetPeer(ctx context.Context, iface string, p PeerSpec) error {
	if err := validateInterfaceName(iface); err != nil {
		return err
	}
	args := []string{"set", iface, "peer", p.PublicKey}
	if p.PresharedKey != "" {
		// `awg set` accepts preshared-key from a file argument.
		f, err := os.CreateTemp("", "awg-psk-*")
		if err != nil {
			return err
		}
		defer func() {
			_ = os.Remove(f.Name())
		}()
		if _, err := f.WriteString(p.PresharedKey); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		args = append(args, "preshared-key", f.Name())
	}
	if len(p.AllowedIPs) > 0 {
		args = append(args, "allowed-ips", joinCSV(p.AllowedIPs))
	}
	if p.Endpoint != "" {
		args = append(args, "endpoint", p.Endpoint)
	}
	if p.KeepaliveSecs > 0 {
		args = append(args, "persistent-keepalive", strconv.Itoa(p.KeepaliveSecs))
	}
	_, _, err := c.run(ctx, c.AwgBinary, args...)
	return err
}

func (c *CLIExecutor) RemovePeer(ctx context.Context, iface, publicKey string) error {
	if err := validateInterfaceName(iface); err != nil {
		return err
	}
	_, _, err := c.run(ctx, c.AwgBinary, "set", iface, "peer", publicKey, "remove")
	return err
}

func (c *CLIExecutor) ShowDump(ctx context.Context, iface string) (InterfaceRuntime, []PeerRuntime, error) {
	if err := validateInterfaceName(iface); err != nil {
		return InterfaceRuntime{}, nil, err
	}
	out, _, err := c.run(ctx, c.AwgBinary, "show", iface, "dump")
	if err != nil {
		return InterfaceRuntime{}, nil, err
	}
	return ParseShowDump(out)
}

func (c *CLIExecutor) ShowConf(ctx context.Context, iface string) (string, error) {
	if err := validateInterfaceName(iface); err != nil {
		return "", err
	}
	out, _, err := c.run(ctx, c.AwgBinary, "showconf", iface)
	return out, err
}

func (c *CLIExecutor) InterfaceUp(ctx context.Context, iface, configPath string) error {
	if err := validateInterfaceName(iface); err != nil {
		return err
	}
	// `awg-quick up` is idempotent only when the link does not exist; check first.
	if _, _, err := c.run(ctx, "ip", "link", "show", iface); err == nil {
		return nil
	}
	if configPath == "" {
		configPath = filepath.Join(c.bootstrapConfigDir(), iface+".conf")
	}
	_, _, err := c.run(ctx, c.AwgQuickBinary, "up", configPath)
	return err
}

func (c *CLIExecutor) InterfaceDown(ctx context.Context, iface string) error {
	if err := validateInterfaceName(iface); err != nil {
		return err
	}
	if _, _, err := c.run(ctx, "ip", "link", "show", iface); err != nil {
		return nil // already down
	}
	_, _, err := c.run(ctx, c.AwgQuickBinary, "down", iface)
	return err
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func validateInterfaceName(iface string) error {
	if !ValidInterfaceName(iface) {
		return fmt.Errorf("awg: invalid interface name %q", iface)
	}
	return nil
}

func joinCSV(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}
