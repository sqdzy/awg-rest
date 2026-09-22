package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/awg-rest/awg-rest/internal/crypto"
	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/render"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// V31NodeOptions describes an explicit, create-only AWG 3.1 node rollout.
// It intentionally does not mutate the bootstrap V2 node or migrate peers.
type V31NodeOptions struct {
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

// V31NodeResult contains only non-secret provisioning metadata.
type V31NodeResult struct {
	TenantID        uuid.UUID `json:"tenant_id"`
	ProfileID       uuid.UUID `json:"profile_id"`
	NodeID          uuid.UUID `json:"node_id"`
	PoolID          uuid.UUID `json:"pool_id"`
	ProfileName     string    `json:"profile_name"`
	NodeHostname    string    `json:"node_hostname"`
	InterfaceName   string    `json:"interface_name"`
	PublicEndpoint  string    `json:"public_endpoint"`
	PoolCIDR        string    `json:"pool_cidr"`
	ConfigPath      string    `json:"config_path"`
	ServerPublicKey string    `json:"server_public_key"`
}

// ProvisionV31Node creates a parallel V3.1 profile/node/pool and its local
// bootstrap config. Database rows are committed atomically; on any transaction
// failure, a config file created during the attempt is removed.
//
// The generated profile mirrors the current Amnezia client installer defaults
// for parameters the installer actually sets. ContentPaddingAddition is left
// unset because the current installer defines a constant but does not assign it
// in generateAwgParameters().
func ProvisionV31Node(ctx context.Context, db *repo.DB, opts V31NodeOptions, logger *slog.Logger) (*V31NodeResult, error) {
	if db == nil || db.Pool == nil {
		return nil, fmt.Errorf("db is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	opts = normalizeV31NodeOptions(opts)
	if err := validateV31NodeOptions(opts); err != nil {
		return nil, err
	}

	poolCIDR, err := parseCIDR(opts.PoolCIDR)
	if err != nil {
		return nil, err
	}
	tenant, err := (&repo.Tenants{DB: db}).GetBySlug(ctx, strings.TrimSpace(opts.TenantSlug))
	if err != nil {
		return nil, fmt.Errorf("tenant %q: %w", opts.TenantSlug, err)
	}

	headerProtectionKey, err := crypto.GeneratePresharedKey()
	if err != nil {
		return nil, fmt.Errorf("generate header-protection key: %w", err)
	}
	serverKP, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate server keypair: %w", err)
	}

	profile := defaultV31Profile(strings.TrimSpace(opts.ProfileName), headerProtectionKey)
	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("generated v3.1 profile invalid: %w", err)
	}

	address, err := serverAddressFromPool(poolCIDR)
	if err != nil {
		return nil, err
	}
	postUp, postDown := natHooks(poolCIDR, opts.NodeIface, opts.EgressIface, opts.EnableNAT)
	config := render.Server(render.Interface{
		PrivateKey: serverKP.PrivateKey,
		Address:    []string{address},
		ListenPort: opts.NodeBasePort,
		PostUp:     postUp,
		PostDown:   postDown,
	}, profile, nil)

	configPath := filepath.Join(opts.BootstrapConfDir, opts.NodeIface+".conf")
	var result V31NodeResult
	var configCreated bool

	err = db.InTx(ctx, func(tx pgx.Tx) error {
		if err := ensureV31ProvisioningAvailable(ctx, tx, opts, poolCIDR); err != nil {
			return err
		}

		profiles := &repo.Profiles{DB: db}
		insertedProfile, err := profiles.InsertTx(ctx, tx, profile)
		if err != nil {
			return fmt.Errorf("insert v3.1 profile: %w", err)
		}

		nodes := &repo.Nodes{DB: db}
		node, err := nodes.InsertTx(ctx, tx, domain.Node{
			ProfileID:       &insertedProfile.ID,
			Region:          opts.NodeRegion,
			Hostname:        opts.NodeHostname,
			PublicEndpoint:  endpointWithPort(opts.NodeEndpoint, opts.NodeBasePort),
			BasePort:        opts.NodeBasePort,
			InterfaceName:   opts.NodeIface,
			ServerPublicKey: serverKP.PublicKey,
		})
		if err != nil {
			return fmt.Errorf("insert v3.1 node: %w", err)
		}

		poolID, err := (&repo.Pools{DB: db}).CreatePoolTx(ctx, tx, tenant.ID, node.ID, poolCIDR)
		if err != nil {
			return fmt.Errorf("insert v3.1 pool: %w", err)
		}

		if err := writeBootstrapConfigExclusive(configPath, config); err != nil {
			return err
		}
		configCreated = true

		result = V31NodeResult{
			TenantID:        tenant.ID,
			ProfileID:       insertedProfile.ID,
			NodeID:          node.ID,
			PoolID:          poolID,
			ProfileName:     insertedProfile.Name,
			NodeHostname:    node.Hostname,
			InterfaceName:   node.InterfaceName,
			PublicEndpoint:  node.PublicEndpoint,
			PoolCIDR:        poolCIDR.String(),
			ConfigPath:      configPath,
			ServerPublicKey: node.ServerPublicKey,
		}
		return nil
	})
	if err != nil {
		if configCreated {
			_ = os.Remove(configPath)
		}
		return nil, err
	}

	logger.InfoContext(ctx, "provisioned parallel awg v3.1 node",
		"node_id", result.NodeID,
		"profile_id", result.ProfileID,
		"interface", result.InterfaceName,
		"endpoint", result.PublicEndpoint,
		"pool", result.PoolCIDR,
	)
	return &result, nil
}

func defaultV31Profile(name, headerProtectionKey string) domain.ProtocolProfile {
	return domain.ProtocolProfile{
		Name:                 name,
		ProtocolVersion:      domain.ProtocolV31,
		Jc:                   5,
		Jmin:                 10,
		Jmax:                 50,
		S1:                   12,
		S2:                   12,
		S3:                   12,
		S4:                   12,
		H1:                   domain.IntRange{Min: 1, Max: 1},
		H2:                   domain.IntRange{Min: 2, Max: 2},
		H3:                   domain.IntRange{Min: 3, Max: 3},
		H4:                   domain.IntRange{Min: 4, Max: 4},
		I1:                   defaultSpecialJunk1,
		HeaderProtectionKey:  headerProtectionKey,
		RekeyAfterTime:       domain.Uint16Range{Min: 100, Max: 120},
		RekeyTimeout:         domain.Uint16Range{Min: 3, Max: 7},
		RejectAfterTime:      domain.Uint16Range{Min: 150, Max: 180},
		KeepaliveTimeout:     domain.Uint16Range{Min: 5, Max: 15},
		MaxHandshakeAttempts: domain.Uint16Range{Min: 15, Max: 20},
		PersistentKeepalive:  domain.Uint16Range{Min: 25, Max: 35},
		RandomTrailers:       true,
		DisableCookies:       true,
		ListenPortPolicy:     "fixed",
	}
}

func normalizeV31NodeOptions(opts V31NodeOptions) V31NodeOptions {
	opts.TenantSlug = strings.TrimSpace(opts.TenantSlug)
	opts.ProfileName = strings.TrimSpace(opts.ProfileName)
	opts.NodeRegion = strings.TrimSpace(opts.NodeRegion)
	opts.NodeHostname = strings.TrimSpace(opts.NodeHostname)
	opts.NodeEndpoint = strings.TrimSpace(opts.NodeEndpoint)
	opts.NodeIface = strings.TrimSpace(opts.NodeIface)
	opts.PoolCIDR = strings.TrimSpace(opts.PoolCIDR)
	opts.BootstrapConfDir = strings.TrimSpace(opts.BootstrapConfDir)
	opts.EgressIface = strings.TrimSpace(opts.EgressIface)
	return opts
}

func validateV31NodeOptions(opts V31NodeOptions) error {
	required := []struct {
		name  string
		value string
	}{
		{"tenant", opts.TenantSlug},
		{"profile-name", opts.ProfileName},
		{"node-region", opts.NodeRegion},
		{"node-hostname", opts.NodeHostname},
		{"node-endpoint", opts.NodeEndpoint},
		{"node-iface", opts.NodeIface},
		{"pool-cidr", opts.PoolCIDR},
		{"bootstrap-conf-dir", opts.BootstrapConfDir},
	}
	for _, item := range required {
		if strings.TrimSpace(item.value) == "" {
			return fmt.Errorf("%s is required", item.name)
		}
	}
	if opts.NodeBasePort < 1 || opts.NodeBasePort > 65535 {
		return fmt.Errorf("node-port must be in [1, 65535]")
	}
	if !awg.ValidInterfaceName(opts.NodeIface) {
		return fmt.Errorf("invalid node-iface %q", opts.NodeIface)
	}
	if opts.EnableNAT && !awg.ValidInterfaceName(opts.EgressIface) {
		return fmt.Errorf("invalid egress-iface %q", opts.EgressIface)
	}
	return nil
}

func ensureV31ProvisioningAvailable(ctx context.Context, tx pgx.Tx, opts V31NodeOptions, pool netip.Prefix) error {
	checks := []struct {
		query string
		arg   any
		what  string
	}{
		{`SELECT EXISTS(SELECT 1 FROM protocol_profiles WHERE name = $1)`, opts.ProfileName, "profile name"},
		{`SELECT EXISTS(SELECT 1 FROM vpn_nodes WHERE hostname = $1)`, opts.NodeHostname, "node hostname"},
		{`SELECT EXISTS(SELECT 1 FROM vpn_nodes WHERE interface_name = $1)`, opts.NodeIface, "interface name"},
		{`SELECT EXISTS(SELECT 1 FROM vpn_nodes WHERE base_port = $1)`, opts.NodeBasePort, "UDP port"},
	}
	for _, check := range checks {
		var exists bool
		if err := tx.QueryRow(ctx, check.query, check.arg).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%s %v is already in use", check.what, check.arg)
		}
	}
	var overlaps bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM address_pools WHERE cidr && $1::cidr)`,
		pool.String(),
	).Scan(&overlaps); err != nil {
		return err
	}
	if overlaps {
		return fmt.Errorf("pool CIDR %s overlaps an existing address pool", pool)
	}
	return nil
}

func writeBootstrapConfigExclusive(path, config string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create bootstrap config dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("bootstrap config %s already exists; refusing to overwrite", path)
		}
		return fmt.Errorf("create bootstrap config: %w", err)
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.WriteString(config); err != nil {
		return fmt.Errorf("write bootstrap config: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync bootstrap config: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close bootstrap config: %w", err)
	}
	ok = true
	return nil
}
