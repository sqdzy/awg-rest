//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/stretchr/testify/require"
)

func TestMigrate_IsIdempotentAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)

	// startPostgres already applied all embedded migrations once. Production
	// binaries call repo.Migrate on every start, so repeated application must
	// remain safe.
	require.NoError(t, repo.Migrate(ctx, db.Pool))
	require.NoError(t, repo.Migrate(ctx, db.Pool))
}

func TestMigrate_BackfillsIdleNodeWhenOnlyOneProfileExists(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	profiles := &repo.Profiles{DB: db}
	nodes := &repo.Nodes{DB: db}

	profile, err := profiles.Insert(ctx, migrationV2Profile("only-profile", 1_000))
	require.NoError(t, err)
	node, err := nodes.Insert(ctx, domain.Node{
		Region: "eu", Hostname: "idle-single.test", PublicEndpoint: "idle-single.test:585",
		BasePort: 585, InterfaceName: "awg9", ServerPublicKey: "server-public",
	})
	require.NoError(t, err)
	require.Nil(t, node.ProfileID)

	require.NoError(t, repo.Migrate(ctx, db.Pool))

	got, err := nodes.GetByID(ctx, node.ID)
	require.NoError(t, err)
	require.NotNil(t, got.ProfileID)
	require.Equal(t, profile.ID, *got.ProfileID)
}

func TestMigrate_RejectsAmbiguousIdleNodeWithMultipleProfiles(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	profiles := &repo.Profiles{DB: db}
	nodes := &repo.Nodes{DB: db}

	_, err := profiles.Insert(ctx, migrationV2Profile("profile-a", 1_000))
	require.NoError(t, err)
	_, err = profiles.Insert(ctx, migrationV2Profile("profile-b", 11_000))
	require.NoError(t, err)
	node, err := nodes.Insert(ctx, domain.Node{
		Region: "eu", Hostname: "idle-ambiguous.test", PublicEndpoint: "idle-ambiguous.test:586",
		BasePort: 586, InterfaceName: "awg8", ServerPublicKey: "server-public",
	})
	require.NoError(t, err)
	require.Nil(t, node.ProfileID)

	err = repo.Migrate(ctx, db.Pool)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no peers and multiple protocol profiles")

	got, getErr := nodes.GetByID(ctx, node.ID)
	require.NoError(t, getErr)
	require.Nil(t, got.ProfileID, "failed migration must not guess a profile")
}

func migrationV2Profile(name string, base int64) domain.ProtocolProfile {
	return domain.ProtocolProfile{
		Name: name, ProtocolVersion: domain.ProtocolV2,
		Jc: 5, Jmin: 10, Jmax: 50, S1: 40, S2: 32,
		H1: domain.IntRange{Min: base, Max: base + 100},
		H2: domain.IntRange{Min: base + 1_000, Max: base + 1_100},
		H3: domain.IntRange{Min: base + 2_000, Max: base + 2_100},
		H4: domain.IntRange{Min: base + 3_000, Max: base + 3_100},
	}
}


func TestMigrate_PreservesLegacyDefaultNodeSelection(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	profiles := &repo.Profiles{DB: db}
	nodes := &repo.Nodes{DB: db}

	profile, err := profiles.Insert(ctx, migrationV2Profile("default-migration-profile", 21_000))
	require.NoError(t, err)
	_, err = nodes.Insert(ctx, domain.Node{
		ProfileID: &profile.ID,
		Region: "eu", Hostname: "zeta-node.test", PublicEndpoint: "zeta-node.test:588",
		BasePort: 588, InterfaceName: "awgz", ServerPublicKey: "server-public-z",
	})
	require.NoError(t, err)
	alpha, err := nodes.Insert(ctx, domain.Node{
		ProfileID: &profile.ID,
		Region: "eu", Hostname: "alpha-node.test", PublicEndpoint: "alpha-node.test:589",
		BasePort: 589, InterfaceName: "awga", ServerPublicKey: "server-public-a",
	})
	require.NoError(t, err)

	// Migration 0004 had already run while the DB was empty. Re-running the
	// migration after legacy nodes exist must mark the same alphabetically
	// first node that pre-rollout PickFirst would have selected.
	require.NoError(t, repo.Migrate(ctx, db.Pool))

	picked, err := nodes.PickFirst(ctx)
	require.NoError(t, err)
	require.Equal(t, alpha.ID, picked.ID)
	require.True(t, picked.IsDefault)

	var defaults int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM vpn_nodes WHERE is_default`).Scan(&defaults))
	require.Equal(t, 1, defaults)
}

func TestNodes_RejectMultipleExplicitDefaults(t *testing.T) {
	ctx := context.Background()
	db := startPostgres(ctx, t)
	profiles := &repo.Profiles{DB: db}
	nodes := &repo.Nodes{DB: db}

	profile, err := profiles.Insert(ctx, migrationV2Profile("unique-default-profile", 31_000))
	require.NoError(t, err)
	_, err = nodes.Insert(ctx, domain.Node{
		ProfileID: &profile.ID, IsDefault: true,
		Region: "eu", Hostname: "default-one.test", PublicEndpoint: "default-one.test:590",
		BasePort: 590, InterfaceName: "awg1", ServerPublicKey: "server-public-1",
	})
	require.NoError(t, err)
	_, err = nodes.Insert(ctx, domain.Node{
		ProfileID: &profile.ID, IsDefault: true,
		Region: "eu", Hostname: "default-two.test", PublicEndpoint: "default-two.test:591",
		BasePort: 591, InterfaceName: "awg2", ServerPublicKey: "server-public-2",
	})
	require.Error(t, err)
}
