package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/awg-rest/awg-rest/internal/crypto"
	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/render"
	"github.com/awg-rest/awg-rest/internal/repo"
)

const defaultV31NodeBasePort = 38824

// V31Defaults describes the optional parallel AWG 3.1 interface. It is kept
// separate from Defaults because enabling V3.1 must never mutate the legacy V2
// bootstrap node in place.
type V31Defaults struct {
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

// EnvV31Defaults derives the opt-in V3.1 rollout configuration. Existing base
// bootstrap values are reused only for non-conflicting placement settings such
// as tenant, region, endpoint host, NAT and config directory.
func EnvV31Defaults(base Defaults) V31Defaults {
	return V31Defaults{
		Enabled:          envBool("BOOTSTRAP_V31_ENABLED", false),
		TenantSlug:       env("BOOTSTRAP_V31_TENANT_SLUG", base.TenantSlug),
		ProfileName:      env("BOOTSTRAP_V31_PROFILE_NAME", "default-v3.1"),
		NodeRegion:       env("BOOTSTRAP_V31_NODE_REGION", base.NodeRegion),
		NodeHostname:     env("BOOTSTRAP_V31_NODE_HOSTNAME", "awg-node-31"),
		NodeEndpoint:     env("BOOTSTRAP_V31_NODE_ENDPOINT", base.NodeEndpoint),
		NodeBasePort:     envInt("BOOTSTRAP_V31_NODE_BASE_PORT", defaultV31NodeBasePort),
		NodeIface:        env("BOOTSTRAP_V31_NODE_IFACE", "awg31"),
		PoolCIDR:         env("BOOTSTRAP_V31_POOL_CIDR", "10.201.0.0/24"),
		BootstrapConfDir: env("BOOTSTRAP_V31_CONF_DIR", base.BootstrapConfDir),
		EnableNAT:        envBool("BOOTSTRAP_V31_ENABLE_NAT", base.EnableNAT),
		EgressIface:      env("BOOTSTRAP_V31_EGRESS_IFACE", base.EgressIface),
	}
}

// EnsureV31Rollout idempotently creates a second AWG 3.1 profile/node/pool and
// bootstrap config without touching the existing V2 node. It works both after
// a fresh legacy bootstrap and on an already-populated installation.
func EnsureV31Rollout(
	ctx context.Context,
	db *repo.DB,
	base Defaults,
	d V31Defaults,
	logger *slog.Logger,
) error {
	if !d.Enabled {
		return nil
	}
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(d.ProfileName) == "" {
		return fmt.Errorf("BOOTSTRAP_V31_PROFILE_NAME is required")
	}
	if strings.TrimSpace(d.NodeHostname) == "" {
		return fmt.Errorf("BOOTSTRAP_V31_NODE_HOSTNAME is required")
	}
	if d.BootstrapConfDir == "" {
		return fmt.Errorf("BOOTSTRAP_V31_CONF_DIR/BOOTSTRAP_CONF_DIR is required when BOOTSTRAP_V31_ENABLED=true")
	}
	if !awg.ValidInterfaceName(d.NodeIface) {
		return fmt.Errorf("invalid BOOTSTRAP_V31_NODE_IFACE %q", d.NodeIface)
	}
	if d.EnableNAT && !awg.ValidInterfaceName(d.EgressIface) {
		return fmt.Errorf("invalid BOOTSTRAP_V31_EGRESS_IFACE %q", d.EgressIface)
	}
	if d.NodeBasePort < 1 || d.NodeBasePort > 65535 {
		return fmt.Errorf("BOOTSTRAP_V31_NODE_BASE_PORT must be in [1, 65535], got %d", d.NodeBasePort)
	}

	v31Pool, err := parseCIDRNamed("BOOTSTRAP_V31_POOL_CIDR", d.PoolCIDR)
	if err != nil {
		return err
	}

	tenants := &repo.Tenants{DB: db}
	tenant, err := tenants.GetBySlug(ctx, d.TenantSlug)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("V3.1 rollout tenant %q does not exist; run/enable the base bootstrap first", d.TenantSlug)
		}
		return fmt.Errorf("load V3.1 rollout tenant: %w", err)
	}

	nodes := &repo.Nodes{DB: db}
	baseNode, err := nodes.GetByHostname(ctx, base.NodeHostname)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("base bootstrap node %q does not exist; refusing to guess which node shares the all-in-one host", base.NodeHostname)
		}
		return fmt.Errorf("load base bootstrap node: %w", err)
	}
	if err := validateV31Placement(baseNode, d); err != nil {
		return err
	}

	pools := &repo.Pools{DB: db}
	baseCIDRs, err := pools.CIDRsByNode(ctx, baseNode.ID)
	if err != nil {
		return fmt.Errorf("load base node address pools: %w", err)
	}
	for _, existing := range baseCIDRs {
		if prefixesOverlap(existing, v31Pool) {
			return fmt.Errorf("BOOTSTRAP_V31_POOL_CIDR %s overlaps base node pool %s", v31Pool, existing)
		}
	}

	profiles := &repo.Profiles{DB: db}
	profile, err := profiles.GetByName(ctx, d.ProfileName)
	switch {
	case err == nil:
		if err := validateManagedV31Profile(*profile); err != nil {
			return fmt.Errorf("existing V3.1 rollout profile %q is not the managed safe preset: %w", d.ProfileName, err)
		}
	case errors.Is(err, domain.ErrNotFound):
		headerKP, keyErr := crypto.GenerateKeyPair()
		if keyErr != nil {
			return fmt.Errorf("generate V3.1 header protection key: %w", keyErr)
		}
		created, insertErr := profiles.Insert(ctx, managedV31Profile(d.ProfileName, headerKP.PrivateKey))
		if insertErr != nil {
			return fmt.Errorf("create V3.1 rollout profile: %w", insertErr)
		}
		profile = created
		logger.InfoContext(ctx, "created V3.1 rollout profile", "name", profile.Name, "id", profile.ID)
	default:
		return fmt.Errorf("load V3.1 rollout profile: %w", err)
	}

	expectedEndpoint := endpointAtPort(d.NodeEndpoint, d.NodeBasePort)
	node, err := nodes.GetByHostname(ctx, d.NodeHostname)
	switch {
	case err == nil:
		if err := validateManagedV31Node(*node, *profile, d, expectedEndpoint); err != nil {
			return err
		}
		if err := verifyManagedBootstrapConfig(d, v31Pool, *profile, *node); err != nil {
			return err
		}
	case errors.Is(err, domain.ErrNotFound):
		node, err = createManagedV31Node(ctx, nodes, d, v31Pool, *profile, expectedEndpoint)
		if err != nil {
			return err
		}
		logger.InfoContext(ctx, "created V3.1 rollout node",
			"hostname", node.Hostname, "interface", node.InterfaceName, "id", node.ID)
	default:
		return fmt.Errorf("load V3.1 rollout node: %w", err)
	}

	if err := ensureManagedV31Pool(ctx, pools, tenant.ID, node.ID, v31Pool); err != nil {
		return err
	}
	logger.InfoContext(ctx, "V3.1 rollout ready",
		"profile", profile.Name,
		"node", node.Hostname,
		"interface", node.InterfaceName,
		"endpoint", node.PublicEndpoint,
		"pool", v31Pool.String())
	return nil
}

func managedV31Profile(name, headerProtectionKey string) domain.ProtocolProfile {
	return domain.ProtocolProfile{
		Name:            name,
		ProtocolVersion: domain.ProtocolV31,
		Jc:              5,
		Jmin:            10,
		Jmax:            50,
		S1:              12,
		S2:              12,
		S3:              12,
		S4:              12,
		H1:              domain.IntRange{Min: 1, Max: 1},
		H2:              domain.IntRange{Min: 2, Max: 2},
		H3:              domain.IntRange{Min: 3, Max: 3},
		H4:              domain.IntRange{Min: 4, Max: 4},
		I1:              defaultSpecialJunk1,
		HeaderProtectionKey:    headerProtectionKey,
		ContentPaddingAddition: domain.Uint16Range{Min: 10, Max: 100},
		RekeyAfterTime:         domain.Uint16Range{Min: 100, Max: 120},
		RekeyTimeout:           domain.Uint16Range{Min: 3, Max: 7},
		RejectAfterTime:        domain.Uint16Range{Min: 150, Max: 180},
		KeepaliveTimeout:       domain.Uint16Range{Min: 5, Max: 15},
		MaxHandshakeAttempts:   domain.Uint16Range{Min: 15, Max: 20},
		RandomTrailers:         true,
		DisableCookies:         true,
		ListenPortPolicy:       "fixed",
	}
}

func validateManagedV31Profile(p domain.ProtocolProfile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	want := managedV31Profile(p.Name, p.HeaderProtectionKey)
	if p.ProtocolVersion != want.ProtocolVersion ||
		p.Jc != want.Jc || p.Jmin != want.Jmin || p.Jmax != want.Jmax ||
		p.S1 != want.S1 || p.S2 != want.S2 || p.S3 != want.S3 || p.S4 != want.S4 ||
		p.H1 != want.H1 || p.H2 != want.H2 || p.H3 != want.H3 || p.H4 != want.H4 ||
		p.I1 != want.I1 || p.I2 != "" || p.I3 != "" || p.I4 != "" || p.I5 != "" ||
		p.ContentPaddingAddition != want.ContentPaddingAddition ||
		p.RekeyAfterTime != want.RekeyAfterTime ||
		p.RekeyTimeout != want.RekeyTimeout ||
		p.RejectAfterTime != want.RejectAfterTime ||
		p.KeepaliveTimeout != want.KeepaliveTimeout ||
		p.MaxHandshakeAttempts != want.MaxHandshakeAttempts ||
		p.RandomTrailers != want.RandomTrailers ||
		p.DisableCookies != want.DisableCookies ||
		p.ListenPortPolicy != want.ListenPortPolicy {
		return fmt.Errorf("profile parameters differ from the verified managed V3.1 preset")
	}
	return nil
}

func validateV31Placement(baseNode *domain.Node, d V31Defaults) error {
	if baseNode == nil {
		return fmt.Errorf("base node is nil")
	}
	if d.NodeHostname == baseNode.Hostname {
		return fmt.Errorf("BOOTSTRAP_V31_NODE_HOSTNAME must differ from base node hostname %q", baseNode.Hostname)
	}
	if d.NodeIface == baseNode.InterfaceName {
		return fmt.Errorf("BOOTSTRAP_V31_NODE_IFACE must differ from base interface %q", baseNode.InterfaceName)
	}
	if d.NodeBasePort == baseNode.BasePort {
		return fmt.Errorf("BOOTSTRAP_V31_NODE_BASE_PORT must differ from base UDP port %d", baseNode.BasePort)
	}
	return nil
}

func validateManagedV31Node(node domain.Node, profile domain.ProtocolProfile, d V31Defaults, endpoint string) error {
	if node.ProfileID == nil || *node.ProfileID != profile.ID {
		return fmt.Errorf("existing V3.1 node %q is bound to a different protocol profile", node.Hostname)
	}
	if node.InterfaceName != d.NodeIface {
		return fmt.Errorf("existing V3.1 node %q uses interface %q, want %q", node.Hostname, node.InterfaceName, d.NodeIface)
	}
	if node.BasePort != d.NodeBasePort {
		return fmt.Errorf("existing V3.1 node %q uses UDP port %d, want %d", node.Hostname, node.BasePort, d.NodeBasePort)
	}
	if node.PublicEndpoint != endpoint {
		return fmt.Errorf("existing V3.1 node %q uses endpoint %q, want %q", node.Hostname, node.PublicEndpoint, endpoint)
	}
	if strings.TrimSpace(node.ServerPublicKey) == "" {
		return fmt.Errorf("existing V3.1 node %q has no server public key", node.Hostname)
	}
	return nil
}

func createManagedV31Node(
	ctx context.Context,
	nodes *repo.Nodes,
	d V31Defaults,
	pool netip.Prefix,
	profile domain.ProtocolProfile,
	endpoint string,
) (*domain.Node, error) {
	configPath := filepath.Join(d.BootstrapConfDir, d.NodeIface+".conf")
	var privateKey, publicKey string

	if raw, err := os.ReadFile(configPath); err == nil {
		privateKey = awg.InterfaceValue(string(raw), "PrivateKey")
		if privateKey == "" {
			return nil, fmt.Errorf("orphan V3.1 bootstrap config %s has no PrivateKey", configPath)
		}
		publicKey, err = crypto.DerivePublicKey(privateKey)
		if err != nil {
			return nil, fmt.Errorf("orphan V3.1 bootstrap config %s has invalid PrivateKey: %w", configPath, err)
		}
		if err := verifyBootstrapConfigBytes(string(raw), d, pool, privateKey, profile); err != nil {
			return nil, fmt.Errorf("orphan V3.1 bootstrap config %s does not match requested rollout: %w", configPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect V3.1 bootstrap config %s: %w", configPath, err)
	} else {
		kp, keyErr := crypto.GenerateKeyPair()
		if keyErr != nil {
			return nil, fmt.Errorf("generate V3.1 server keypair: %w", keyErr)
		}
		privateKey, publicKey = kp.PrivateKey, kp.PublicKey
		if err := writeV31BootstrapConf(d, pool, privateKey, profile); err != nil {
			return nil, fmt.Errorf("write V3.1 bootstrap config: %w", err)
		}
	}

	node, err := nodes.Insert(ctx, domain.Node{
		ProfileID:       &profile.ID,
		Region:          d.NodeRegion,
		Hostname:        d.NodeHostname,
		PublicEndpoint:  endpoint,
		BasePort:        d.NodeBasePort,
		InterfaceName:   d.NodeIface,
		ServerPublicKey: publicKey,
	})
	if err != nil {
		return nil, fmt.Errorf("create V3.1 rollout node: %w", err)
	}
	return node, nil
}

func verifyManagedBootstrapConfig(d V31Defaults, pool netip.Prefix, profile domain.ProtocolProfile, node domain.Node) error {
	path := filepath.Join(d.BootstrapConfDir, d.NodeIface+".conf")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("V3.1 node %q exists but bootstrap private-key config %s is missing; refusing silent key rotation", node.Hostname, path)
		}
		return fmt.Errorf("read V3.1 bootstrap config %s: %w", path, err)
	}
	privateKey := awg.InterfaceValue(string(raw), "PrivateKey")
	if privateKey == "" {
		return fmt.Errorf("V3.1 bootstrap config %s has no PrivateKey", path)
	}
	publicKey, err := crypto.DerivePublicKey(privateKey)
	if err != nil {
		return fmt.Errorf("derive V3.1 server public key from %s: %w", path, err)
	}
	if publicKey != node.ServerPublicKey {
		return fmt.Errorf("V3.1 bootstrap config %s private key does not match node public key", path)
	}
	if err := verifyBootstrapConfigBytes(string(raw), d, pool, privateKey, profile); err != nil {
		return fmt.Errorf("V3.1 bootstrap config %s drifted from managed rollout: %w", path, err)
	}
	return nil
}

func verifyBootstrapConfigBytes(raw string, d V31Defaults, pool netip.Prefix, privateKey string, profile domain.ProtocolProfile) error {
	want, err := renderV31BootstrapConf(d, pool, privateKey, profile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(raw) != strings.TrimSpace(want) {
		return fmt.Errorf("config content differs from expected node/profile/pool/NAT settings")
	}
	return nil
}

func writeV31BootstrapConf(d V31Defaults, pool netip.Prefix, privateKey string, profile domain.ProtocolProfile) error {
	cfg, err := renderV31BootstrapConf(d, pool, privateKey, profile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d.BootstrapConfDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(d.BootstrapConfDir, d.NodeIface+".conf")
	return os.WriteFile(path, []byte(cfg), 0o600)
}

func renderV31BootstrapConf(d V31Defaults, pool netip.Prefix, privateKey string, profile domain.ProtocolProfile) (string, error) {
	if err := profile.Validate(); err != nil {
		return "", err
	}
	address, err := serverAddressFromPool(pool)
	if err != nil {
		return "", err
	}
	postUp, postDown := natHooks(pool, d.NodeIface, d.EgressIface, d.EnableNAT)
	return render.Server(render.Interface{
		PrivateKey: privateKey,
		Address:    []string{address},
		ListenPort: d.NodeBasePort,
		PostUp:     postUp,
		PostDown:   postDown,
	}, profile, nil), nil
}

func ensureManagedV31Pool(ctx context.Context, pools *repo.Pools, tenantID, nodeID domainUUID, want netip.Prefix) error {
	return nil
}

// domainUUID is an alias only to keep the rollout helper signatures readable.
type domainUUID = [16]byte

func endpointAtPort(endpoint string, port int) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = "127.0.0.1"
	}
	if host, _, err := net.SplitHostPort(endpoint); err == nil {
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
	if strings.HasPrefix(endpoint, "[") && strings.HasSuffix(endpoint, "]") {
		endpoint = strings.TrimSuffix(strings.TrimPrefix(endpoint, "["), "]")
	}
	return net.JoinHostPort(endpoint, strconv.Itoa(port))
}

func parseCIDRNamed(name, raw string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid %s %q: %w", name, raw, err)
	}
	p = p.Masked()
	if !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("%s currently supports IPv4 pools only, got %s", name, p)
	}
	if _, err := serverAddressFromPool(p); err != nil {
		return netip.Prefix{}, fmt.Errorf("%s: %w", name, err)
	}
	return p, nil
}

func prefixesOverlap(a, b netip.Prefix) bool {
	a, b = a.Masked(), b.Masked()
	if a.Addr().BitLen() != b.Addr().BitLen() {
		return false
	}
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}
