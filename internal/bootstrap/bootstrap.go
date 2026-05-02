// Package bootstrap seeds a blank database with a default tenant, profile, node,
// and address pool so the control plane is usable immediately after the first
// start ("plug-n-play").  It is idempotent: repeated runs are no-ops.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/awg-rest/awg-rest/internal/crypto"
	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/render"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/jackc/pgx/v5"
)

const (
	defaultNodeBasePort = 38823
	defaultSpecialJunk1 = "<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>"
)

// Defaults holds the seed configuration driven by environment variables.
type Defaults struct {
	Enabled          bool
	TenantSlug       string
	ProfileName      string
	NodeRegion       string
	NodeHostname     string
	NodeEndpoint     string
	NodeBasePort     int
	NodeIface        string
	PoolCIDR         string
	BootstrapConfDir string
	EnableNAT        bool
	EgressIface      string
}

// EnvDefaults returns Defaults populated from env.
func EnvDefaults() Defaults {
	return Defaults{
		Enabled:          envBool("BOOTSTRAP_ENABLED", false),
		TenantSlug:       env("BOOTSTRAP_TENANT_SLUG", "default"),
		ProfileName:      env("BOOTSTRAP_PROFILE_NAME", "default-v2"),
		NodeRegion:       env("BOOTSTRAP_NODE_REGION", "default"),
		NodeHostname:     env("BOOTSTRAP_NODE_HOSTNAME", "awg-node-1"),
		NodeEndpoint:     env("BOOTSTRAP_NODE_ENDPOINT", "127.0.0.1"),
		NodeBasePort:     envInt("BOOTSTRAP_NODE_BASE_PORT", defaultNodeBasePort),
		NodeIface:        env("BOOTSTRAP_NODE_IFACE", "awg0"),
		PoolCIDR:         env("BOOTSTRAP_POOL_CIDR", "10.200.0.0/24"),
		BootstrapConfDir: env("BOOTSTRAP_CONF_DIR", ""),
		EnableNAT:        envBool("BOOTSTRAP_ENABLE_NAT", true),
		EgressIface:      env("BOOTSTRAP_EGRESS_IFACE", "eth0"),
	}
}

// RunIfEmpty seeds default tenant, profile, node and pool only when the
// corresponding tables have zero rows.  It also writes the node bootstrap
// interface config so the node-agent can bring the interface up before the
// first peer is created.
func RunIfEmpty(ctx context.Context, db *repo.DB, d Defaults, logger *slog.Logger) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !d.Enabled {
		logger.InfoContext(ctx, "bootstrap disabled")
		return nil
	}
	if d.BootstrapConfDir == "" {
		return fmt.Errorf("BOOTSTRAP_CONF_DIR is required when BOOTSTRAP_ENABLED=true")
	}
	poolCIDR, err := parseCIDR(d.PoolCIDR)
	if err != nil {
		return err
	}

	// Tenant
	var tenantID interface{}
	row := db.Pool.QueryRow(ctx, `SELECT id FROM tenants LIMIT 1`)
	if err := row.Scan(&tenantID); err == nil {
		logger.InfoContext(ctx, "bootstrap skipped: tenant exists")
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("bootstrap inspect tenants: %w", err)
	}

	tenants := &repo.Tenants{DB: db}
	t, err := tenants.Upsert(ctx, d.TenantSlug)
	if err != nil {
		return fmt.Errorf("bootstrap tenant: %w", err)
	}
	logger.InfoContext(ctx, "bootstrapped tenant", "slug", d.TenantSlug, "id", t.ID)

	// Profile (V2 with AmneziaVPN-compatible defaults).  The ranges stay below
	// signed int32 max because several clients and tools document or generate
	// H1-H4 values in that compatibility band.
	profiles := &repo.Profiles{DB: db}
	profile, err := profiles.Insert(ctx, domain.ProtocolProfile{
		Name:             d.ProfileName,
		ProtocolVersion:  domain.ProtocolV2,
		Jc:               5,
		Jmin:             10,
		Jmax:             50,
		S1:               130,
		S2:               89,
		S3:               31,
		S4:               18,
		H1:               domain.IntRange{Min: 1820352565, Max: 1967349318},
		H2:               domain.IntRange{Min: 2027388662, Max: 2115196205},
		H3:               domain.IntRange{Min: 2144626841, Max: 2145335687},
		H4:               domain.IntRange{Min: 2146302274, Max: 2147385353},
		I1:               defaultSpecialJunk1,
		ListenPortPolicy: "fixed",
	})
	if err != nil {
		return fmt.Errorf("bootstrap profile: %w", err)
	}
	logger.InfoContext(ctx, "bootstrapped profile", "name", d.ProfileName, "id", profile.ID)

	// Node
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("bootstrap keygen: %w", err)
	}

	nodes := &repo.Nodes{DB: db}
	node, err := nodes.Insert(ctx, domain.Node{
		Region:          d.NodeRegion,
		Hostname:        d.NodeHostname,
		PublicEndpoint:  endpointWithPort(d.NodeEndpoint, d.NodeBasePort),
		BasePort:        d.NodeBasePort,
		InterfaceName:   d.NodeIface,
		ServerPublicKey: kp.PublicKey,
	})
	if err != nil {
		return fmt.Errorf("bootstrap node: %w", err)
	}
	logger.InfoContext(ctx, "bootstrapped node", "hostname", d.NodeHostname, "id", node.ID)

	// Pool
	pools := &repo.Pools{DB: db}
	poolID, err := pools.CreatePool(ctx, t.ID, node.ID, poolCIDR)
	if err != nil {
		return fmt.Errorf("bootstrap pool: %w", err)
	}
	logger.InfoContext(ctx, "bootstrapped pool", "cidr", d.PoolCIDR, "id", poolID)

	// Write bootstrap interface config (private key only) so node-agent can
	// `awg-quick up` before the first syncconf.
	if err := writeBootstrapConf(d, poolCIDR, kp.PrivateKey, *profile); err != nil {
		return fmt.Errorf("bootstrap conf: %w", err)
	}

	return nil
}

func writeBootstrapConf(d Defaults, pool netip.Prefix, serverPrivKey string, profile domain.ProtocolProfile) error {
	if !awg.ValidInterfaceName(d.NodeIface) {
		return fmt.Errorf("invalid BOOTSTRAP_NODE_IFACE %q", d.NodeIface)
	}
	if d.EnableNAT && !awg.ValidInterfaceName(d.EgressIface) {
		return fmt.Errorf("invalid BOOTSTRAP_EGRESS_IFACE %q", d.EgressIface)
	}
	address, err := serverAddressFromPool(pool)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d.BootstrapConfDir, 0o700); err != nil {
		return err
	}
	path := d.BootstrapConfDir + "/" + d.NodeIface + ".conf"
	postUp, postDown := natHooks(pool, d.NodeIface, d.EgressIface, d.EnableNAT)
	cfg := render.Server(render.Interface{
		PrivateKey: serverPrivKey,
		Address:    []string{address},
		ListenPort: d.NodeBasePort,
		PostUp:     postUp,
		PostDown:   postDown,
	}, profile, nil)
	return os.WriteFile(path, []byte(cfg), 0o600)
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		var i int
		if _, err := fmt.Sscanf(v, "%d", &i); err == nil {
			return i
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}

func parseCIDR(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid BOOTSTRAP_POOL_CIDR %q: %w", s, err)
	}
	return p.Masked(), nil
}

func endpointWithPort(hostport string, port int) string {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		hostport = "127.0.0.1"
	}
	if _, _, err := net.SplitHostPort(hostport); err == nil {
		return hostport
	}
	return net.JoinHostPort(hostport, strconv.Itoa(port))
}

func serverAddressFromPool(pool netip.Prefix) (string, error) {
	pool = pool.Masked()
	if !pool.Addr().Is4() {
		return "", fmt.Errorf("bootstrap currently supports IPv4 pools only, got %s", pool)
	}
	addr := pool.Addr().Next()
	if !addr.IsValid() || !pool.Contains(addr) {
		return "", fmt.Errorf("BOOTSTRAP_POOL_CIDR %s is too small for a server address", pool)
	}
	return fmt.Sprintf("%s/%d", addr, pool.Bits()), nil
}

func natHooks(pool netip.Prefix, iface, egress string, enabled bool) (string, string) {
	if !enabled {
		return "", ""
	}
	cidr := pool.String()
	upRules := []string{
		fmt.Sprintf("iptables -C FORWARD -i %s -o %s -j ACCEPT || iptables -A FORWARD -i %s -o %s -j ACCEPT", iface, egress, iface, egress),
		fmt.Sprintf("iptables -C FORWARD -i %s -o %s -m state --state RELATED,ESTABLISHED -j ACCEPT || iptables -A FORWARD -i %s -o %s -m state --state RELATED,ESTABLISHED -j ACCEPT", egress, iface, egress, iface),
		fmt.Sprintf("iptables -t nat -C POSTROUTING -s %s -o %s -j MASQUERADE || iptables -t nat -A POSTROUTING -s %s -o %s -j MASQUERADE", cidr, egress, cidr, egress),
	}
	downRules := []string{
		fmt.Sprintf("iptables -D FORWARD -i %s -o %s -j ACCEPT 2>/dev/null || true", iface, egress),
		fmt.Sprintf("iptables -D FORWARD -i %s -o %s -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true", egress, iface),
		fmt.Sprintf("iptables -t nat -D POSTROUTING -s %s -o %s -j MASQUERADE 2>/dev/null || true", cidr, egress),
	}
	return strings.Join(upRules, "; "), strings.Join(downRules, "; ")
}
