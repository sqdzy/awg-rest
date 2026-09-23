//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/stretchr/testify/require"
)

func TestMigrate_UpgradesLegacyV2SchemaAndPreservesPeer(t *testing.T) {
	ctx := context.Background()
	db := startPostgresRaw(ctx, t)

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	legacyPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations", "0001_init.up.sql")
	legacySQL, err := os.ReadFile(legacyPath)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, string(legacySQL))
	require.NoError(t, err)

	var tenantID, nodeID, profileID, peerID string
	require.NoError(t, db.Pool.QueryRow(ctx,
		`INSERT INTO tenants(slug) VALUES ('legacy') RETURNING id::text`).Scan(&tenantID))
	require.NoError(t, db.Pool.QueryRow(ctx, `
INSERT INTO vpn_nodes(region, hostname, public_endpoint, base_port, interface_name, server_public_key)
VALUES ('eu','legacy-vpn.test','legacy-vpn.test:585',585,'awg0','legacy-server-key')
RETURNING id::text`).Scan(&nodeID))
	require.NoError(t, db.Pool.QueryRow(ctx, `
INSERT INTO protocol_profiles(
    name, protocol_version, jc, jmin, jmax, s1, s2, s3, s4,
    h1_min, h1_max, h2_min, h2_max, h3_min, h3_max, h4_min, h4_max
) VALUES (
    'legacy-v2','v2',5,10,50,40,32,12,12,
    1000,1100,2000,2100,3000,3100,4000,4100
) RETURNING id::text`).Scan(&profileID))
	require.NoError(t, db.Pool.QueryRow(ctx, `
INSERT INTO peers(
    tenant_id, node_id, profile_id, external_id, public_key, preshared_key_ref, allowed_ip
) VALUES ($1::uuid,$2::uuid,$3::uuid,'legacy-peer','legacy-public','legacy-psk','10.200.0.2')
RETURNING id::text`, tenantID, nodeID, profileID).Scan(&peerID))

	require.NoError(t, repo.Migrate(ctx, db.Pool))

	var migratedProfileID string
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT profile_id::text FROM vpn_nodes WHERE id=$1::uuid`, nodeID).Scan(&migratedProfileID))
	require.Equal(t, profileID, migratedProfileID)

	var gotPeerID, gotPeerProfile string
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT id::text, profile_id::text FROM peers WHERE id=$1::uuid`, peerID).
		Scan(&gotPeerID, &gotPeerProfile))
	require.Equal(t, peerID, gotPeerID)
	require.Equal(t, profileID, gotPeerProfile)

	var version string
	var headerKey *string
	require.NoError(t, db.Pool.QueryRow(ctx,
		`SELECT protocol_version, header_protection_key FROM protocol_profiles WHERE id=$1::uuid`,
		profileID).Scan(&version, &headerKey))
	require.Equal(t, "v2", version)
	require.Nil(t, headerKey)
}

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
