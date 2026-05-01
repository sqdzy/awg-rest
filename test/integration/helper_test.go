//go:build integration

// Package integration runs Postgres-backed tests via testcontainers-go.
// Run with `go test -tags=integration ./test/integration/...`.
package integration

import (
	"context"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/awg-rest/awg-rest/internal/domain"
	"github.com/awg-rest/awg-rest/internal/repo"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startPostgres spins up a fresh Postgres for one test, applies migrations,
// and registers a cleanup hook. Returns the configured DB.
func startPostgres(ctx context.Context, t *testing.T) *repo.DB {
	t.Helper()
	disableRyukForLocalTests(t)
	image := "postgres:15-alpine"
	pg, err := tcpostgres.Run(ctx,
		image,
		tcpostgres.WithDatabase("awg"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(pg) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := repo.NewDB(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(db.Close)

	require.NoError(t, repo.Migrate(ctx, db.Pool))
	return db
}

func disableRyukForLocalTests(t *testing.T) {
	t.Helper()
	if os.Getenv("TESTCONTAINERS_RYUK_DISABLED") == "" {
		t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}
}

// seedFixtures creates a tenant, profile, node, and an address pool. Returns
// their IDs for use in tests.
type fixtures struct {
	TenantID, NodeID, ProfileID, PoolID uuid.UUID
}

func seedFixtures(ctx context.Context, t *testing.T, db *repo.DB) fixtures {
	t.Helper()
	tenants := &repo.Tenants{DB: db}
	profiles := &repo.Profiles{DB: db}
	nodes := &repo.Nodes{DB: db}
	pools := &repo.Pools{DB: db}

	ten, err := tenants.Upsert(ctx, "acme")
	require.NoError(t, err)

	prof, err := profiles.Insert(ctx, domain.ProtocolProfile{
		Name: "default-v2", ProtocolVersion: domain.ProtocolV2,
		Jc: 5, Jmin: 64, Jmax: 1000, S1: 40, S2: 32,
		H1: domain.IntRange{Min: 1_000, Max: 2_000},
		H2: domain.IntRange{Min: 3_000, Max: 4_000},
		H3: domain.IntRange{Min: 5_000, Max: 6_000},
		H4: domain.IntRange{Min: 7_000, Max: 8_000},
	})
	require.NoError(t, err)

	node, err := nodes.Insert(ctx, domain.Node{
		Region: "eu", Hostname: "vpn-1.test", PublicEndpoint: "vpn-1.test:585",
		BasePort: 585, InterfaceName: "awg0",
		ServerPublicKey: "U2VydmVyUHVibGljS2V5MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY=",
	})
	require.NoError(t, err)

	poolCidr, _ := netip.ParsePrefix("10.90.0.0/24")
	poolID, err := pools.CreatePool(ctx, ten.ID, node.ID, poolCidr)
	require.NoError(t, err)

	return fixtures{TenantID: ten.ID, NodeID: node.ID, ProfileID: prof.ID, PoolID: poolID}
}
