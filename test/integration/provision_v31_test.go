//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/awg-rest/awg-rest/internal/bootstrap"
	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/obs"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/stretchr/testify/require"
)

func TestProvisionV31Node_CreateOnlyAndAtomic(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	tenant, err := (&repo.Tenants{DB: db}).Upsert(ctx, "default")
	require.NoError(t, err)

	legacyProfile, err := (&repo.Profiles{DB: db}).Insert(ctx, migrationV2Profile("legacy-default-v2", 20_000))
	require.NoError(t, err)
	legacyNode, err := (&repo.Nodes{DB: db}).Insert(ctx, domain.Node{
		ProfileID:       &legacyProfile.ID,
		Region:          "eu",
		Hostname:        "zz-legacy-v2.test",
		PublicEndpoint:  "203.0.113.30:38823",
		BasePort:        38823,
		InterfaceName:   "awg0",
		ServerPublicKey: "legacy-server-public",
	})
	require.NoError(t, err)

	dir := t.TempDir()
	opts := bootstrap.V31NodeOptions{
		TenantSlug:       tenant.Slug,
		ProfileName:      "default-v31",
		NodeRegion:       "eu",
		NodeHostname:     "vpn-v31.test",
		NodeEndpoint:     "203.0.113.31",
		NodeBasePort:     38824,
		NodeIface:        "awg31",
		PoolCIDR:         "10.201.0.0/24",
		BootstrapConfDir: dir,
		EnableNAT:        false,
		EgressIface:      "eth0",
	}

	got, err := bootstrap.ProvisionV31Node(ctx, db, opts, obs.NewLogger("error", false))
	require.NoError(t, err)
	require.Equal(t, "awg31", got.InterfaceName)
	require.Equal(t, "203.0.113.31:38824", got.PublicEndpoint)
	require.Equal(t, "10.201.0.0/24", got.PoolCIDR)
	require.NotEmpty(t, got.ServerPublicKey)

	picked, err := (&repo.Nodes{DB: db}).PickFirst(ctx)
	require.NoError(t, err)
	require.Equal(t, legacyNode.ID, picked.ID,
		"parallel v3.1 node must not become implicit default while a legacy node exists")

	profile, err := (&repo.Profiles{DB: db}).GetByID(ctx, got.ProfileID)
	require.NoError(t, err)
	require.Equal(t, domain.ProtocolV31, profile.ProtocolVersion)
	require.Equal(t, domain.IntRange{Min: 1, Max: 1}, profile.H1)
	require.Equal(t, domain.IntRange{Min: 4, Max: 4}, profile.H4)
	require.Equal(t, domain.Uint16Range{Min: 100, Max: 120}, profile.RekeyAfterTime)
	require.Equal(t, domain.Uint16Range{Min: 25, Max: 35}, profile.PersistentKeepalive)
	require.True(t, profile.RandomTrailers)
	require.True(t, profile.DisableCookies)
	require.True(t, profile.ContentPaddingAddition.IsZero(),
		"current Amnezia installer does not assign ContentPaddingAddition")

	node, err := (&repo.Nodes{DB: db}).GetByID(ctx, got.NodeID)
	require.NoError(t, err)
	require.NotNil(t, node.ProfileID)
	require.Equal(t, got.ProfileID, *node.ProfileID)

	raw, err := os.ReadFile(got.ConfigPath)
	require.NoError(t, err)
	cfg := string(raw)
	require.Contains(t, cfg, "PrivateKey = ")
	require.Contains(t, cfg, "HeaderProtectionKey = ")
	require.Contains(t, cfg, "RekeyAfterTime = 100-120")
	require.Contains(t, cfg, "RandomTrailers = on")
	require.Contains(t, cfg, "DisableCookies = on")
	require.NotContains(t, cfg, "ContentPaddingAddition = ")

	info, err := os.Stat(got.ConfigPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	_, err = bootstrap.ProvisionV31Node(ctx, db, opts, obs.NewLogger("error", false))
	require.Error(t, err, "provisioning is create-only and must refuse duplicates")

	overlap := opts
	overlap.ProfileName = "second-v31"
	overlap.NodeHostname = "vpn-v31-2.test"
	overlap.NodeIface = "awg32"
	overlap.NodeBasePort = 38825
	overlap.PoolCIDR = "10.201.0.128/25"
	_, err = bootstrap.ProvisionV31Node(ctx, db, overlap, obs.NewLogger("error", false))
	require.Error(t, err)
	require.Contains(t, err.Error(), "overlaps")
	require.NoFileExists(t, filepath.Join(dir, "awg32.conf"))

	var profileCount, nodeCount int
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM protocol_profiles WHERE name IN ('default-v31','second-v31')`).Scan(&profileCount))
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM vpn_nodes WHERE hostname IN ('vpn-v31.test','vpn-v31-2.test')`).Scan(&nodeCount))
	require.Equal(t, 1, profileCount, "failed transaction must not leave a partial profile")
	require.Equal(t, 1, nodeCount, "failed transaction must not leave a partial node")
}

func TestProvisionV31Node_ConcurrentCommandsCannotReuseUDPPort(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	tenant, err := (&repo.Tenants{DB: db}).Upsert(ctx, "default")
	require.NoError(t, err)

	dir := t.TempDir()
	first := bootstrap.V31NodeOptions{
		TenantSlug: tenant.Slug, ProfileName: "concurrent-a",
		NodeRegion: "eu", NodeHostname: "node-concurrent-a.test",
		NodeEndpoint: "203.0.113.50", NodeBasePort: 38824,
		NodeIface: "awg31a", PoolCIDR: "10.211.0.0/24",
		BootstrapConfDir: dir,
	}
	second := first
	second.ProfileName = "concurrent-b"
	second.NodeHostname = "node-concurrent-b.test"
	second.NodeIface = "awg31b"
	second.PoolCIDR = "10.212.0.0/24"

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, opts := range []bootstrap.V31NodeOptions{first, second} {
		opts := opts
		go func() {
			<-start
			_, provisionErr := bootstrap.ProvisionV31Node(ctx, db, opts, obs.NewLogger("error", false))
			results <- provisionErr
		}()
	}
	close(start)
	err1, err2 := <-results, <-results
	successes := 0
	for _, attemptErr := range []error{err1, err2} {
		if attemptErr == nil {
			successes++
		} else {
			require.Contains(t, attemptErr.Error(), "UDP port",
				"the competing operator should see the deterministic port conflict")
		}
	}
	require.Equal(t, 1, successes, "only one concurrent provisioning may commit")

	var nodes, profiles, pools int
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM vpn_nodes WHERE base_port=38824`).Scan(&nodes))
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM protocol_profiles WHERE name IN ('concurrent-a', 'concurrent-b')`).Scan(&profiles))
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM address_pools WHERE cidr IN ('10.211.0.0/24', '10.212.0.0/24')`).Scan(&pools))
	require.Equal(t, 1, nodes)
	require.Equal(t, 1, profiles)
	require.Equal(t, 1, pools)

	_, statA := os.Stat(filepath.Join(dir, "awg31a.conf"))
	_, statB := os.Stat(filepath.Join(dir, "awg31b.conf"))
	require.NotEqual(t, statA == nil, statB == nil,
		"only the winning transaction may retain its local bootstrap file")
	if statA != nil { require.True(t, os.IsNotExist(statA), "%v", statA) }
	if statB != nil { require.True(t, os.IsNotExist(statB), "%v", statB) }
}
