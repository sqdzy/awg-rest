//go:build integration

package integration

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/awg-rest/awg-rest/internal/awg"
	"github.com/awg-rest/awg-rest/internal/bootstrap"
	"github.com/awg-rest/awg-rest/internal/crypto"
	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/stretchr/testify/require"
)

func TestV31Rollout_ProvisionsParallelNodeAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := rolloutBaseDefaults(dir)
	v31 := rolloutV31Defaults(dir)

	require.NoError(t, bootstrap.RunIfEmpty(ctx, db, base, logger))
	require.NoError(t, bootstrap.EnsureV31Rollout(ctx, db, base, v31, logger))

	nodes := &repo.Nodes{DB: db}
	profiles := &repo.Profiles{DB: db}
	pools := &repo.Pools{DB: db}

	baseNode, err := nodes.GetByHostname(ctx, base.NodeHostname)
	require.NoError(t, err)
	require.True(t, baseNode.IsDefault)

	v31Node, err := nodes.GetByHostname(ctx, v31.NodeHostname)
	require.NoError(t, err)
	require.False(t, v31Node.IsDefault)
	require.True(t, v31Node.AcceptNewPeers)
	require.Equal(t, "vpn.example.test:38824", v31Node.PublicEndpoint)
	require.Equal(t, v31.NodeBasePort, v31Node.BasePort)
	require.Equal(t, v31.NodeIface, v31Node.InterfaceName)

	picked, err := nodes.PickFirst(ctx)
	require.NoError(t, err)
	require.Equal(t, baseNode.ID, picked.ID, "enabling V3.1 must not change unpinned peer placement")

	require.NotNil(t, v31Node.ProfileID)
	profile, err := profiles.GetByID(ctx, *v31Node.ProfileID)
	require.NoError(t, err)
	require.Equal(t, domain.ProtocolV31, profile.ProtocolVersion)
	require.Equal(t, domain.IntRange{Min: 1, Max: 1}, profile.H1)
	require.Equal(t, domain.IntRange{Min: 4, Max: 4}, profile.H4)
	require.Equal(t, 12, profile.S1)
	require.Equal(t, 12, profile.S4)
	require.Equal(t, domain.Uint16Range{Min: 10, Max: 100}, profile.ContentPaddingAddition)
	require.True(t, profile.RandomTrailers)
	require.True(t, profile.DisableCookies)
	require.NoError(t, crypto.ValidateKey(profile.HeaderProtectionKey))

	cidrs, err := pools.CIDRsByNode(ctx, v31Node.ID)
	require.NoError(t, err)
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix(v31.PoolCIDR)}, cidrs)

	configPath := filepath.Join(dir, v31.NodeIface+".conf")
	raw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	info, err := os.Stat(configPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	privateKey := awg.InterfaceValue(string(raw), "PrivateKey")
	require.NotEmpty(t, privateKey)
	publicKey, err := crypto.DerivePublicKey(privateKey)
	require.NoError(t, err)
	require.Equal(t, v31Node.ServerPublicKey, publicKey)
	require.Equal(t, profile.HeaderProtectionKey, awg.InterfaceValue(string(raw), "HeaderProtectionKey"))
	require.Equal(t, "38824", awg.InterfaceValue(string(raw), "ListenPort"))
	require.Equal(t, "10.201.0.1/24", awg.InterfaceValue(string(raw), "Address"))

	var beforeProfiles, beforeNodes, beforePools int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM protocol_profiles`).Scan(&beforeProfiles))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM vpn_nodes`).Scan(&beforeNodes))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM address_pools`).Scan(&beforePools))

	require.NoError(t, bootstrap.EnsureV31Rollout(ctx, db, base, v31, logger))

	var afterProfiles, afterNodes, afterPools int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM protocol_profiles`).Scan(&afterProfiles))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM vpn_nodes`).Scan(&afterNodes))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM address_pools`).Scan(&afterPools))
	require.Equal(t, beforeProfiles, afterProfiles)
	require.Equal(t, beforeNodes, afterNodes)
	require.Equal(t, beforePools, afterPools)

	again, err := nodes.GetByHostname(ctx, v31.NodeHostname)
	require.NoError(t, err)
	require.Equal(t, v31Node.ID, again.ID)
	require.Equal(t, v31Node.ServerPublicKey, again.ServerPublicKey)
}

func TestV31Rollout_RefusesMissingExistingPrivateKeyConfig(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := rolloutBaseDefaults(dir)
	v31 := rolloutV31Defaults(dir)

	require.NoError(t, bootstrap.RunIfEmpty(ctx, db, base, logger))
	require.NoError(t, bootstrap.EnsureV31Rollout(ctx, db, base, v31, logger))
	require.NoError(t, os.Remove(filepath.Join(dir, v31.NodeIface+".conf")))

	err := bootstrap.EnsureV31Rollout(ctx, db, base, v31, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing silent key rotation")
}

func TestV31Rollout_RefusesManagedProfileDrift(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := rolloutBaseDefaults(dir)
	v31 := rolloutV31Defaults(dir)

	require.NoError(t, bootstrap.RunIfEmpty(ctx, db, base, logger))
	require.NoError(t, bootstrap.EnsureV31Rollout(ctx, db, base, v31, logger))

	_, err := db.Pool.Exec(ctx,
		`UPDATE protocol_profiles SET random_trailers = FALSE WHERE name = $1`,
		v31.ProfileName)
	require.NoError(t, err)

	err = bootstrap.EnsureV31Rollout(ctx, db, base, v31, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "managed safe preset")
}

func rolloutBaseDefaults(dir string) bootstrap.Defaults {
	return bootstrap.Defaults{
		Enabled:          true,
		TenantSlug:       "rollout",
		ProfileName:      "default-v2",
		NodeRegion:       "eu",
		NodeHostname:     "awg-node-1",
		NodeEndpoint:     "vpn.example.test:38823",
		NodeBasePort:     38823,
		NodeIface:        "awg0",
		PoolCIDR:         "10.200.0.0/24",
		BootstrapConfDir: dir,
		EnableNAT:        false,
		AcceptNewPeers:   true,
		EgressIface:      "eth0",
	}
}

func rolloutV31Defaults(dir string) bootstrap.V31Defaults {
	return bootstrap.V31Defaults{
		Enabled:          true,
		TenantSlug:       "rollout",
		ProfileName:      "default-v3.1",
		NodeRegion:       "eu",
		NodeHostname:     "awg-node-31",
		NodeEndpoint:     "vpn.example.test:38823",
		NodeBasePort:     38824,
		NodeIface:        "awg31",
		PoolCIDR:         "10.201.0.0/24",
		BootstrapConfDir: dir,
		EnableNAT:        false,
		EgressIface:      "eth0",
	}
}


func TestV31Rollout_DrainAndReenableProvisioning(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	base := rolloutBaseDefaults(dir)
	v31 := rolloutV31Defaults(dir)

	require.NoError(t, bootstrap.RunIfEmpty(ctx, db, base, logger))
	require.NoError(t, bootstrap.EnsureV31Rollout(ctx, db, base, v31, logger))

	nodes := &repo.Nodes{DB: db}
	node, err := nodes.GetByHostname(ctx, v31.NodeHostname)
	require.NoError(t, err)
	require.True(t, node.AcceptNewPeers)

	v31.AcceptNewPeers = false
	require.NoError(t, bootstrap.EnsureV31Rollout(ctx, db, base, v31, logger))
	node, err = nodes.GetByHostname(ctx, v31.NodeHostname)
	require.NoError(t, err)
	require.False(t, node.AcceptNewPeers)

	v31.AcceptNewPeers = true
	require.NoError(t, bootstrap.EnsureV31Rollout(ctx, db, base, v31, logger))
	node, err = nodes.GetByHostname(ctx, v31.NodeHostname)
	require.NoError(t, err)
	require.True(t, node.AcceptNewPeers)

	// Removing the rollout override maps to Enabled=false. Existing state is
	// preserved, but new V3.1 placement is closed automatically.
	v31.Enabled = false
	require.NoError(t, bootstrap.EnsureV31Rollout(ctx, db, base, v31, logger))
	node, err = nodes.GetByHostname(ctx, v31.NodeHostname)
	require.NoError(t, err)
	require.False(t, node.AcceptNewPeers)
}
