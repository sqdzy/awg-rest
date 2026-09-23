// Package awg provides an abstraction over the AmneziaWG userspace tooling
// (`awg`, `awg-quick`, `amneziawg-go`). The Executor interface is implemented by:
//
//   - CLIExecutor — invokes the real binaries on a Linux host (used by the
//     node-agent in production).
//   - FakeExecutor — pure in-memory simulation used by unit/integration tests
//     and for cross-platform development on Windows/macOS workstations where
//     the kernel module is unavailable.
package awg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// PeerSpec is a minimal description of a peer the executor must enforce on a
// running AmneziaWG interface.
type PeerSpec struct {
	PublicKey     string
	PresharedKey  string
	AllowedIPs    []string // CIDRs
	Endpoint      string   // optional
	KeepaliveSecs int      // 0 = off
}

// PeerRuntime is parsed out of `awg show <iface> dump`.
type PeerRuntime struct {
	PublicKey     string
	PresharedKey  string
	Endpoint      string
	AllowedIPs    []string
	LastHandshake time.Time
	RxBytes       int64
	TxBytes       int64
	KeepaliveSecs int
}

// InterfaceRuntime is the interface-level row of `awg show <iface> dump`.
type InterfaceRuntime struct {
	PrivateKey string
	PublicKey  string
	ListenPort int
	FwMark     int
}

// Executor is the runtime contract used by the worker / reconciler.
type Executor interface {
	// SyncConf rewrites the runtime to match the rendered config text without
	// dropping live peer sessions (mirrors `awg syncconf`). The implementation
	// MUST be idempotent: applying the same config twice is a no-op.
	SyncConf(ctx context.Context, iface string, config string) error

	// SetPeer adds or updates a single peer (mirrors `awg set <iface> peer ...`).
	SetPeer(ctx context.Context, iface string, p PeerSpec) error

	// RemovePeer removes a peer by public key.
	RemovePeer(ctx context.Context, iface string, publicKey string) error

	// ShowDump returns runtime state for the given interface.
	ShowDump(ctx context.Context, iface string) (InterfaceRuntime, []PeerRuntime, error)

	// ShowConf returns the canonical effective configuration for the interface
	// (mirrors `awg showconf`).
	ShowConf(ctx context.Context, iface string) (string, error)

	// InterfaceUp ensures the interface exists and is up (`awg-quick up <iface>`
	// when configured under /etc/amnezia/...). Idempotent.
	InterfaceUp(ctx context.Context, iface string, configPath string) error

	// InterfaceDown brings the interface down. Idempotent.
	InterfaceDown(ctx context.Context, iface string) error
}

// HashConfig returns a stable hex SHA-256 over a rendered configuration; used
// by the reconciler to skip no-op apply paths.
func HashConfig(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ErrNotImplemented is returned by platform-specific executors when invoked
// on an unsupported OS.
var ErrNotImplemented = errors.New("awg: executor not implemented on this platform")

// ValidInterfaceName accepts the conservative Linux interface-name subset used
// by this control plane. Linux IFNAMSIZ allows up to 15 visible bytes.
func ValidInterfaceName(name string) bool {
	if name == "" || len(name) > 15 || name == "." || name == ".." {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.':
		default:
			return false
		}
	}
	return true
}
