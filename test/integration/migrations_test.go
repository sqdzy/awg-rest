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
